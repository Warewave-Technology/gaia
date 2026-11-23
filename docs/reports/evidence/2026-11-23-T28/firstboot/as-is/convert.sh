set -eu
vw=/virtio-win.iso
VIRTIO_WIN=$vw virt-v2v -i disk /in/disk.qcow2 -o local -os /out -of qcow2 -on gaia-t28-fb-as-is
