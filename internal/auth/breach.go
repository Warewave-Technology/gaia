package auth

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultBreachURL is the k-anonymity range API of Have I Been Pwned: only the first 5 hex digits of the password's
// SHA-1 leave Gaia, and the answer lists the suffixes of every breached password with that prefix.
const DefaultBreachURL = "https://api.pwnedpasswords.com/range/"

// Breached asks a k-anonymity range API (ADR-008 §6, off by default: Gaia must work air-gapped) whether p appears in a
// known breach. An error means the check could not be made; the caller refuses the password then, since an
// unchecked password is not a checked one.
func Breached(ctx context.Context, client *http.Client, base, p string) (bool, error) {
	sum := sha1.Sum([]byte(p))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+h[:5], nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Add-Padding", "true") // the answer's size does not tell the prefix's count either
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("breach check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("breach check: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		suffix, count, _ := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if strings.EqualFold(suffix, h[5:]) && count != "0" { // padding lines have the count 0
			return true, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("breach check: %w", err)
	}
	return false, nil
}
