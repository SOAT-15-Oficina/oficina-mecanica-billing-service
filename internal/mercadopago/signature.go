package mercadopago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func Manifest(dataID, requestID, ts string) string {
	return "id:" + dataID + ";request-id:" + requestID + ";ts:" + ts + ";"
}

func Sign(secret, dataID, requestID, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(Manifest(dataID, requestID, ts)))
	return hex.EncodeToString(mac.Sum(nil))
}

func SignatureHeader(secret, dataID, requestID, ts string) string {
	return "ts=" + ts + ",v1=" + Sign(secret, dataID, requestID, ts)
}

func VerifySignature(secret, header, dataID, requestID string) bool {
	if secret == "" || header == "" || dataID == "" {
		return false
	}
	var ts, v1 string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ts":
			ts = strings.TrimSpace(value)
		case "v1":
			v1 = strings.TrimSpace(value)
		}
	}
	if ts == "" || v1 == "" {
		return false
	}
	got, err := hex.DecodeString(v1)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(Sign(secret, dataID, requestID, ts))
	return hmac.Equal(got, want)
}
