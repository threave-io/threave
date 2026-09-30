package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/threave-io/threave/internal/notifications"
	"github.com/threave-io/threave/internal/store"
)

type notificationPublicKeyResponse struct {
	PublicKey string `json:"public_key"`
	Supported bool   `json:"supported"`
}

type notificationSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type deleteNotificationSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
}

type notificationStateResponse struct {
	Enabled      bool   `json:"enabled"`
	EndpointHash string `json:"endpoint_hash,omitempty"`
}

type notificationTestResponse struct {
	Sent bool `json:"sent"`
}

func (api API) notificationPublicKeyHandler(w http.ResponseWriter, r *http.Request) {
	publicKey, err := api.notifications.PublicKey(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, notificationPublicKeyResponse{
		PublicKey: publicKey,
		Supported: true,
	})
}

func (api API) saveNotificationSubscriptionHandler(w http.ResponseWriter, r *http.Request) {
	var request notificationSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	_, err := api.notifications.SaveSubscription(r.Context(), notifications.SubscriptionInput{
		Endpoint:  strings.TrimSpace(request.Endpoint),
		P256DH:    strings.TrimSpace(request.Keys.P256DH),
		Auth:      strings.TrimSpace(request.Keys.Auth),
		UserAgent: r.UserAgent(),
		Origin:    requestOrigin(r),
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, notificationStateResponse{Enabled: true})
}

func (api API) deleteNotificationSubscriptionHandler(w http.ResponseWriter, r *http.Request) {
	var request deleteNotificationSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := api.notifications.DeleteSubscription(r.Context(), strings.TrimSpace(request.Endpoint)); err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, notificationStateResponse{Enabled: false})
}

func (api API) testNotificationHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.notifications.SendBadgeVariantTest(r.Context(), r.URL.Query().Get("variant")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, notificationTestResponse{Sent: true})
}

func (api API) acknowledgeNotificationHandler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Endpoint  string `json:"endpoint"`
		SessionID string `json:"session_id"`
		Seq       int64  `json:"seq"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := api.notifications.Acknowledge(r.Context(), request.Endpoint, request.SessionID, request.Seq); err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to acknowledge notification")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"acknowledged": true})
}

func requestOrigin(r *http.Request) string {
	if origin := cleanOrigin(r.Header.Get("Origin")); origin != "" {
		return origin
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return cleanOrigin(proto + "://" + host)
}

func cleanOrigin(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func formatAPITime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func (api API) seenNotificationHandler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string `json:"session_id"`
		Seq       int64  `json:"seq"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := api.notifications.Seen(r.Context(), request.SessionID, request.Seq); err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to acknowledge viewed completion")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"acknowledged": true})
}
