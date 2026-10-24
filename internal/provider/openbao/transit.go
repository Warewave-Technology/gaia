package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Encrypt encrypts plaintext with a transit key; the key never leaves
// OpenBao.
func (c *Client) Encrypt(ctx context.Context, key string, plaintext []byte) (string, error) {
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	err := c.post(ctx, "/v1/transit/encrypt/"+key, map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)}, &out)
	if err != nil {
		return "", err
	}
	return out.Data.Ciphertext, nil
}

// Decrypt decrypts what Encrypt returned.
func (c *Client) Decrypt(ctx context.Context, key, ciphertext string) ([]byte, error) {
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/v1/transit/decrypt/"+key, map[string]string{"ciphertext": ciphertext}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data.Plaintext)
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", c.Token())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openbao: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openbao %s: status %d", path, resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}
