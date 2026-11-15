package mercadopago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManifestFollowsMercadoPagoFormat(t *testing.T) {
	assert.Equal(t, "id:123;request-id:req-1;ts:1700000000;", Manifest("123", "req-1", "1700000000"))
}

func TestSignIsHMACSHA256OfManifest(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("id:123;request-id:req-1;ts:1700000000;"))
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), Sign("secret", "123", "req-1", "1700000000"))
}

func TestVerifySignature(t *testing.T) {
	header := SignatureHeader("secret", "123", "req-1", "1700000000")
	assert.True(t, VerifySignature("secret", header, "123", "req-1"))
	assert.True(t, VerifySignature("secret", " v1="+Sign("secret", "123", "req-1", "1700000000")+" , ts=1700000000", "123", "req-1"))

	cases := map[string]struct{ secret, header, dataID, requestID string }{
		"wrong secret":     {"other", header, "123", "req-1"},
		"other data id":    {"secret", header, "124", "req-1"},
		"other request id": {"secret", header, "123", "req-2"},
		"empty secret":     {"", header, "123", "req-1"},
		"empty header":     {"secret", "", "123", "req-1"},
		"empty data id":    {"secret", header, "", "req-1"},
		"missing ts":       {"secret", "v1=" + Sign("secret", "123", "req-1", ""), "123", "req-1"},
		"missing v1":       {"secret", "ts=1700000000", "123", "req-1"},
		"malformed part":   {"secret", "garbage", "123", "req-1"},
		"non hex v1":       {"secret", "ts=1700000000,v1=zz", "123", "req-1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.False(t, VerifySignature(c.secret, c.header, c.dataID, c.requestID))
		})
	}
}

func TestManifestOmitsMissingRequestID(t *testing.T) {
	assert.Equal(t, "id:ordtst1;ts:1700000000;", Manifest("ordtst1", "", "1700000000"))
}

func TestVerifySignatureAcceptsLowercaseOrOriginalID(t *testing.T) {
	lower := SignatureHeader("secret", "ordtst01abc", "req-1", "1700000000")
	assert.True(t, VerifySignature("secret", lower, "ORDTST01ABC", "req-1"))

	original := SignatureHeader("secret", "ORDTST01ABC", "req-1", "1700000000")
	assert.True(t, VerifySignature("secret", original, "ORDTST01ABC", "req-1"))

	withoutRequest := SignatureHeader("secret", "ordtst01abc", "", "1700000000")
	assert.True(t, VerifySignature("secret", withoutRequest, "ORDTST01ABC", ""))
	assert.False(t, VerifySignature("secret", withoutRequest, "ORDTST01ABC", "req-1"))
}
