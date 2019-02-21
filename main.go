package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"log"
	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	annotationKey = "snapshot-datasource"
)

type WebhookServer struct {
	server           *http.Server
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func main() {
	var port int
	var certFile string
	var keyFile string

	flag.IntVar(&port, "port", 443, "Webhook server port.")
	flag.StringVar(&certFile, "cert", os.Getenv("TLS_CERT_FILE"), "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&keyFile, "key", os.Getenv("TLS_KEY_FILE"), "File containing the x509 private key to -cert.")
	flag.Parse()

	tlsConfig := tls.Config{}

	if certFile != "" && keyFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Printf("Filed to load key pair: %v", err)
		}

		tlsConfig.Certificates = []tls.Certificate{pair}
	}

	w := &WebhookServer{}
	w.server = &http.Server{
		Addr:      fmt.Sprintf(":%v", port),
		TLSConfig: &tlsConfig,
		Handler: w,
	}

	if certFile != "" && keyFile != "" {
		log.Printf("Started TLS server.")
		log.Fatal(w.server.ListenAndServeTLS("", ""))
	} else {
		log.Printf("Started server.")
		log.Fatal(w.server.ListenAndServe())
	}
}

func createPatch(pvc corev1.PersistentVolumeClaim) ([]byte, error) {	
	annotations := pvc.ObjectMeta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	dataSource := strings.Split(annotations[annotationKey], "/")

	patch := []patchOperation{}

	if len(dataSource) > 1 {
		patch = append(patch, patchOperation{
			Op: "add",
			Path: "/spec/dataSource",
			Value: &corev1.TypedLocalObjectReference{
				APIGroup: &dataSource[0],
				Kind: dataSource[1],
				Name: dataSource[2],
			},
		})
	}

	return json.Marshal(patch)
}

// main mutation process
func (w *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
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
