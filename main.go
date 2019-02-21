package main

import (
	"k8s.io/client-go/rest"
	"io/ioutil"
	"crypto/x509"
	"crypto/x509/pkix"
	"crypto/tls"
	"crypto/rsa"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"
	"net/http"
	b64 "encoding/base64"
	"log"
	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	certificatesv1beta1 "k8s.io/api/certificates/v1beta1"
	registrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/kubernetes"
	csrutils "k8s.io/client-go/util/certificate/csr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/meta"
)

const (
	annotationKey = "latest-snapshot"
)

type WebhookServer struct {
	server           *http.Server
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

func (cm *CertificateManager) GenerateKey() {

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
	if err != nil && ! errors.IsAlreadyExists(err) {
		return err
	} else if err != nil {
		existingMwc, err := mwcs.Get("snapshot-webhook", metav1.GetOptions{})
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

	secret, err := clientset.CoreV1().Secrets("snapshot-webhook").Get("snapshot-webhook", metav1.GetOptions{})
	if err != nil && errors.IsNotFound(err) {
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			log.Fatal(err)
		}

		csrTemplate := x509.CertificateRequest{
			Subject: pkix.Name{
				CommonName: "snapshot-webhook.snapshot-webhook.svc",
			},
			SignatureAlgorithm: x509.SHA512WithRSA,
			DNSNames: []string{
				"snapshot-webhook", "snapshot-webhook.snapshot-webhook",
				"snapshot-webhook.snapshot-webhook.svc",
				"snapshot-webhook.snapshot-webhook.svc.cluster",
				"snapshot-webhook.snapshot-webhook.svc.cluster.local",
			},
		}
		csrCertificate, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, priv)
		if err != nil {
			log.Fatal(err)
		}

		csrs := clientset.Certificates().CertificateSigningRequests()

		csrEncoded := pem.EncodeToMemory(&pem.Block{
			Type: "CERTIFICATE REQUEST",
			Bytes: csrCertificate,
		})

		req, err := csrutils.RequestCertificate(csrs, csrEncoded, "snapshot-webhook", []certificatesv1beta1.KeyUsage{
			certificatesv1beta1.UsageDigitalSignature,
			certificatesv1beta1.UsageKeyEncipherment,
			certificatesv1beta1.UsageServerAuth,
		}, priv)
		if err != nil {
			log.Fatal(err)
		}

		log.Println("Waiting for certificate to be authorized, run: kubectl certificate approve snapshot-webhook")
		cert, err := csrutils.WaitForCertificate(csrs, req, time.Second * 60)
		if err != nil {
			log.Fatal(err)
		}

		privateKey := pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(priv),
		})

		secret, err = clientset.CoreV1().Secrets("snapshot-webhook").Create(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "snapshot-webhook",
				Namespace: "snapshot-webhook",
			},
			Data: map[string][]byte{
				"key.pem": []byte(b64.StdEncoding.EncodeToString(privateKey)),
				"cert.pem": []byte(b64.StdEncoding.EncodeToString(cert)),
			},
		})
		if err != nil && ! errors.IsAlreadyExists(err) {
			log.Fatal(err)
		}
		log.Println("Generated new certificate and stored in Secret.")
	} else if err != nil {
		log.Fatal(err)
	} else {
		log.Println("Loaded existing certificate from Secret.")
	}

	cert, err := b64.StdEncoding.DecodeString(string(secret.Data["cert.pem"]))
	if err != nil {
		log.Fatal(err)
	}

	privateKey, err := b64.StdEncoding.DecodeString(string(secret.Data["key.pem"]))
	if err != nil {
		log.Fatal(err)
	}

	pair, err := tls.X509KeyPair(cert, privateKey)
	if err != nil {
		log.Printf("Filed to load key pair: %v", err)
	}

	w := &WebhookServer{}
	w.server = &http.Server{
		Addr:      fmt.Sprintf(":%v", 8443),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{pair},
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
			Name: "snapshot-webhook",
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
							APIGroups: []string{""},
							APIVersions: []string{"v1"},
							Resources: []string{"persistentvolumeclaims"},
						},
					},
				},
				FailurePolicy: &fail,
				ClientConfig: registrationv1beta1.WebhookClientConfig{
					Service: &registrationv1beta1.ServiceReference{
						Namespace: "snapshot-webhook",
						Name: "snapshot-webhook",
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
			Op: "add",
			Path: "/spec/dataSource",
			Value: &corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind: "VolumeSnapshot",
				Name: annotations[annotationKey],
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
		return &v1beta1.AdmissionResponse {
			Result: &metav1.Status {
				Message: err.Error(),
			},
		}
	}

	log.Printf("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pvc.Name, req.UID, req.Operation, req.UserInfo)
	
	patchBytes, err := createPatch(pvc)
	if err != nil {
		return &v1beta1.AdmissionResponse {
			Result: &metav1.Status {
				Message: err.Error(),
			},
		}
	}
	
	log.Printf("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse {
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
			Result: &metav1.Status {
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
