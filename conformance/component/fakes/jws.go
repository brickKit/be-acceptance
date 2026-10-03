package fakes

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// SigningKey is one asymmetric key of the fake identity provider.
type SigningKey struct {
	Kid  string
	Alg  string // RS256, ES256 or EdDSA
	priv crypto.Signer
}

// GenerateKey makes a fresh key for an allowed algorithm (P5.2).
func GenerateKey(alg string) (*SigningKey, error) {
	var priv crypto.Signer
	var err error
	switch alg {
	case "RS256":
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ES256":
		priv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "EdDSA":
		_, priv, err = ed25519.GenerateKey(rand.Reader)
	default:
		return nil, fmt.Errorf("unsupported alg %s", alg)
	}
	if err != nil {
		return nil, err
	}
	k := &SigningKey{Alg: alg, priv: priv}
	k.Kid = thumbprint(k.JWK())
	return k, nil
}

var b64 = base64.RawURLEncoding

// JWK is the public JWK of the key (RFC 7517), with kid, alg and use.
func (k *SigningKey) JWK() map[string]string {
	m := map[string]string{"alg": k.Alg, "use": "sig"}
	switch pub := k.priv.Public().(type) {
	case *rsa.PublicKey:
		m["kty"], m["n"], m["e"] = "RSA", b64.EncodeToString(pub.N.Bytes()), b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	case *ecdsa.PublicKey:
		m["kty"], m["crv"] = "EC", "P-256"
		m["x"], m["y"] = b64.EncodeToString(pad32(pub.X.Bytes())), b64.EncodeToString(pad32(pub.Y.Bytes()))
	case ed25519.PublicKey:
		m["kty"], m["crv"], m["x"] = "OKP", "Ed25519", b64.EncodeToString(pub)
	}
	if k.Kid != "" {
		m["kid"] = k.Kid
	}
	return m
}

// thumbprint is the RFC 7638 JWK thumbprint over the required members.
func thumbprint(j map[string]string) string {
	var req []string
	switch j["kty"] {
	case "RSA":
		req = []string{"e", "kty", "n"}
	case "EC":
		req = []string{"crv", "kty", "x", "y"}
	default:
		req = []string{"crv", "kty", "x"}
	}
	s := "{"
	for i, r := range req {
		if i > 0 {
			s += ","
		}
		v, _ := json.Marshal(j[r])
		s += `"` + r + `":` + string(v)
	}
	sum := sha256.Sum256([]byte(s + "}"))
	return b64.EncodeToString(sum[:])
}

func pad32(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// sign produces the JWS signature of signingInput.
func (k *SigningKey) sign(input []byte) ([]byte, error) {
	switch p := k.priv.(type) {
	case *rsa.PrivateKey:
		h := sha256.Sum256(input)
		return rsa.SignPKCS1v15(rand.Reader, p, crypto.SHA256, h[:])
	case *ecdsa.PrivateKey:
		h := sha256.Sum256(input)
		r, s, err := ecdsa.Sign(rand.Reader, p, h[:])
		if err != nil {
			return nil, err
		}
		return append(pad32(r.Bytes()), pad32(s.Bytes())...), nil
	case ed25519.PrivateKey:
		return ed25519.Sign(p, input), nil
	}
	return nil, fmt.Errorf("unknown key type")
}

// encodeJWS builds a compact JWS. mode: "" signs with key; "none" leaves the signature
// empty; "hs256" signs with HMAC-SHA256 and secret.
func encodeJWS(header, claims map[string]any, key *SigningKey, mode string, secret []byte) (string, error) {
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	var sig []byte
	switch mode {
	case "none":
	case "hs256":
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(input))
		sig = m.Sum(nil)
	default:
		if sig, err = key.sign([]byte(input)); err != nil {
			return "", err
		}
	}
	return input + "." + b64.EncodeToString(sig), nil
}

// PublicKeyFromJWKS returns the public key with this kid from a JWKS document.
func PublicKeyFromJWKS(doc []byte, kid string) (crypto.PublicKey, error) {
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, err
	}
	for _, j := range set.Keys {
		if j["kid"] != kid {
			continue
		}
		dec := func(s string) []byte { b, _ := b64.DecodeString(s); return b }
		switch j["kty"] {
		case "RSA":
			return &rsa.PublicKey{N: new(big.Int).SetBytes(dec(j["n"])), E: int(new(big.Int).SetBytes(dec(j["e"])).Int64())}, nil
		case "EC":
			return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(dec(j["x"])), Y: new(big.Int).SetBytes(dec(j["y"]))}, nil
		case "OKP":
			return ed25519.PublicKey(dec(j["x"])), nil
		}
	}
	return nil, fmt.Errorf("kid %s not in JWKS", kid)
}
