set -eu
vw=/virtio-win.iso
mkdir /tmp/vw
guestfish --ro --format=raw -a /virtio-win.iso -m /dev/sda tar-out / - | tar -x -C /tmp/vw --ignore-case --exclude=smbus --exclude=smbus.*
echo "left-out driver smbus: $(find /tmp/vw -iname 'smbus*' | wc -l) files still in the tree"
vw=/tmp/vw
VIRTIO_WIN=$vw virt-v2v -i disk /in/disk.qcow2 -o local -os /out -of qcow2 -on gaia-t28-fb-no-smbus
