package cli

import "testing"

func TestLoopbackOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:8080": true,
		"0.0.0.0:8080": false, ":8080": false, "192.168.1.25:8080": false,
	} {
		if err := loopbackOnly(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}
