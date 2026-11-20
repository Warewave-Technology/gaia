package libvirtbackup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/digitalocean/go-libvirt"
)

// libvirt's "checkpoint inconsistent" (a bitmap lost when QEMU stopped without saving it, T24 on server B after the
// lab host went down) is ErrCheckpointInconsistent through the wrapping; any other error is not.
func TestInconsistentCheckpoint(t *testing.T) {
	lerr := libvirt.Error{Code: uint32(libvirt.ErrCheckpointInconsistent), Message: "checkpoint inconsistent: missing or broken bitmap 'gaia-1-1299' for disk 'vda'"}
	wrapped := inconsistent{fmt.Errorf("backup begin: %w", lerr)}
	if !isInconsistent(fmt.Errorf("x: %w", lerr)) || !errors.Is(wrapped, ErrCheckpointInconsistent) || wrapped.Error() != "backup begin: "+lerr.Message {
		t.Fatalf("not recognised: %v", wrapped)
	}
	other := libvirt.Error{Code: uint32(libvirt.ErrNoDomainCheckpoint), Message: "no checkpoint"}
	if isInconsistent(other) || isInconsistent(errors.New("checkpoint inconsistent")) {
		t.Fatal("another error taken for an inconsistent checkpoint")
	}
}
