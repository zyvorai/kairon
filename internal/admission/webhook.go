package admission

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/zyvorai/kairon/internal/model"
)

type AdmissionReview struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Request    *AdmissionRequest  `json:"request,omitempty"`
	Response   *AdmissionResponse `json:"response,omitempty"`
}

type AdmissionRequest struct {
	UID       string          `json:"uid"`
	Object    json.RawMessage `json:"object"`
	Operation string          `json:"operation"`
}

type AdmissionResponse struct {
	UID     string  `json:"uid"`
	Allowed bool    `json:"allowed"`
	Result  *Status `json:"status,omitempty"`
}

type Status struct {
	Message string `json:"message,omitempty"`
}

type Handler struct{}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var review AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil || review.Request == nil {
		http.Error(w, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}
	resp := &AdmissionResponse{UID: review.Request.UID, Allowed: true}
	if review.Request.Operation == "CREATE" || review.Request.Operation == "UPDATE" {
		var m model.Machine
		if err := json.Unmarshal(review.Request.Object, &m); err != nil {
			resp.Allowed = false
			resp.Result = &Status{Message: "unable to decode Machine: " + err.Error()}
		} else if reason := ValidateMachine(m); reason != "" {
			resp.Allowed = false
			resp.Result = &Status{Message: reason}
		}
	}
	out := AdmissionReview{
		APIVersion: review.APIVersion,
		Kind:       review.Kind,
		Response:   resp,
	}
	if out.APIVersion == "" {
		out.APIVersion = "admission.k8s.io/v1"
	}
	if out.Kind == "" {
		out.Kind = "AdmissionReview"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
