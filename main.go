package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	b64 "encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"k8s.io/api/admission/v1beta1"
	registrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	certificatesv1beta1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	csrutils "k8s.io/client-go/util/certificate/csr"
	"log"
	"net/http"
	"time"
)

const (
	annotationKey    = "latest-snapshot"
	webhookNamespace = "snapshot-webhook"
	webhookName      = "snapshot-webhook"
)

type WebhookServer struct {
	server *http.Server
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func init() {
	runtimeScheme := runtime.NewScheme()
	_ = runtime.ObjectDefaulter(runtimeScheme)
	registrationv1beta1.AddToScheme(runtimeScheme)
	certificatesv1beta1.AddToScheme(runtimeScheme)
}

type CertificateManager struct {
	clientset kubernetes.Interface
}

func (cm *CertificateManager) GenerateKey() ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	host := fmt.Sprintf("%s.%s", webhookName, webhookNamespace)

	csrTemplate := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: host + ".svc",
		},
		SignatureAlgorithm: x509.SHA512WithRSA,
		DNSNames: []string{
			webhookName, host, host + ".svc", host + ".svc.cluster",
			host + ".svc.cluster.local",
		},
	}

	csrCertificate, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, priv)
	if err != nil {
		return nil, nil, err
	}

	csrEncoded := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrCertificate,
	})

	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	}), csrEncoded, err
}

func (cm *CertificateManager) GetCA(config *rest.Config) ([]byte, error) {
	caData := config.TLSClientConfig.CAData

	if config.TLSClientConfig.CAFile != "" {
		return ioutil.ReadFile(config.TLSClientConfig.CAFile)
	}

	return caData, nil
}

func (cm *CertificateManager) CreateWebhook(config *rest.Config) error {
	caData, err := cm.GetCA(config)
	if err != nil {
		return err
	}

	mwcs := cm.clientset.Admissionregistration().MutatingWebhookConfigurations()
	mwc := createMutatingWebhookConfiguration(caData)
	_, err = mwcs.Create(mwc)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err != nil {
		existingMwc, err := mwcs.Get(webhookName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		existingMeta, _ := meta.Accessor(existingMwc)
		desiredMeta, _ := meta.Accessor(mwc)
		desiredMeta.SetResourceVersion(existingMeta.GetResourceVersion())

		_, err = mwcs.Update(mwc)
		if err != nil {
			return err
		}
	}

	return nil
}

func (cm *CertificateManager) RequestCertificate(csr []byte, privateKey []byte) ([]byte, error) {
	csrs := cm.clientset.Certificates().CertificateSigningRequests()

	req, err := csrutils.RequestCertificate(csrs, csr, webhookName, []certificatesv1beta1.KeyUsage{
		certificatesv1beta1.UsageDigitalSignature,
		certificatesv1beta1.UsageKeyEncipherment,
		certificatesv1beta1.UsageServerAuth,
	}, privateKey)
	if err != nil {
		return nil, err
	}

	log.Println("Waiting for certificate to be authorized, run: kubectl certificate approve", webhookName)
	return csrutils.WaitForCertificate(csrs, req, time.Second*60)
}

func (cm *CertificateManager) GetCertificate() (tls.Certificate, error) {
	secrets := cm.clientset.CoreV1().Secrets(webhookNamespace)

	secret, err := secrets.Get(webhookName, metav1.GetOptions{})
	if err != nil && errors.IsNotFound(err) {
		privateKey, csrEncoded, err := cm.GenerateKey()
		if err != nil {
			return tls.Certificate{}, err
		}

		cert, err := cm.RequestCertificate(csrEncoded, privateKey)
		if err != nil {
			return tls.Certificate{}, err
		}

		secret, err = secrets.Create(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      webhookName,
				Namespace: webhookNamespace,
			},
			Data: map[string][]byte{
				"key.pem":  []byte(b64.StdEncoding.EncodeToString(privateKey)),
				"cert.pem": []byte(b64.StdEncoding.EncodeToString(cert)),
			},
		})
		if err != nil && !errors.IsAlreadyExists(err) {
			return tls.Certificate{}, err
		}
		log.Println("Generated new certificate and stored in Secret.")
	} else if err != nil {
		return tls.Certificate{}, err
	} else {
		log.Println("Loaded existing certificate from Secret.")
	}

	cert, err := b64.StdEncoding.DecodeString(string(secret.Data["cert.pem"]))
	if err != nil {
		return tls.Certificate{}, err
	}

	privateKey, err := b64.StdEncoding.DecodeString(string(secret.Data["key.pem"]))
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(cert, privateKey)
}

func main() {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	cm := CertificateManager{clientset: clientset}

	err = cm.CreateWebhook(config)
	if err != nil {
		log.Fatal(err)
	}

	keyPair, err := cm.GetCertificate()
	if err != nil {
		log.Fatal(err)
	}

	w := &WebhookServer{}
	w.server = &http.Server{
		Addr: fmt.Sprintf(":%v", 8443),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{keyPair},
		},
		Handler: w,
	}

	log.Printf("Started TLS server.")
	log.Fatal(w.server.ListenAndServeTLS("", ""))
}

func createMutatingWebhookConfiguration(caCert []byte) *registrationv1beta1.MutatingWebhookConfiguration {
	fail := registrationv1beta1.Fail

	return &registrationv1beta1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: webhookName,
		},
		Webhooks: []registrationv1beta1.Webhook{
			registrationv1beta1.Webhook{
				Name: "snapshot-webhook.codesink.net",
				Rules: []registrationv1beta1.RuleWithOperations{
					registrationv1beta1.RuleWithOperations{
						Operations: []registrationv1beta1.OperationType{
							registrationv1beta1.Create,
						},
						Rule: registrationv1beta1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"persistentvolumeclaims"},
						},
					},
				},
				FailurePolicy: &fail,
				ClientConfig: registrationv1beta1.WebhookClientConfig{
					Service: &registrationv1beta1.ServiceReference{
						Namespace: webhookNamespace,
						Name:      webhookName,
					},
					CABundle: []byte(caCert),
				},
			},
		},
	}
}

func createPatch(pvc corev1.PersistentVolumeClaim) ([]byte, error) {
	annotations := pvc.ObjectMeta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	patch := []patchOperation{}

	if annotations[annotationKey] != "" {
		apiGroup := "snapshot.storage.k8s.io"

		patch = append(patch, patchOperation{
			Op:   "add",
			Path: "/spec/dataSource",
			Value: &corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "VolumeSnapshot",
				Name:     annotations[annotationKey],
			},
		})
	}

	return json.Marshal(patch)
}

// main mutation process
func (ws *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var pvc corev1.PersistentVolumeClaim
	if err := json.Unmarshal(req.Object.Raw, &pvc); err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	log.Printf("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pvc.Name, req.UID, req.Operation, req.UserInfo)

	patchBytes, err := createPatch(pvc)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	log.Printf("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

// Serve method for webhook server
func (ws *WebhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var ar v1beta1.AdmissionReview
	var admissionResponse *v1beta1.AdmissionResponse

	err := json.NewDecoder(r.Body).Decode(&ar)
	if err != nil {
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = ws.mutate(&ar)
	}

	admissionReview := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}

	if _, err := w.Write(resp); err != nil {
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
