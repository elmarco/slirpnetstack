import subprocess
import socket
import os
import sys
from shlex import split, quote

a, b = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)

slirp_cmd = 'bin/slirpnetstack --pcap /tmp/slirpnetstack-%d.pcap --fd %d' % (os.getpid(), a.fileno())

qemu_cmd = 'qemu-system-x86_64 -enable-kvm -smp 4 -m 4G -cpu host -device virtio-net-pci,netdev=net0 -netdev socket,fd=%d,id=net0 -snapshot %s' % (b.fileno(), quote(sys.argv[1]))

slirp = subprocess.Popen(split(slirp_cmd), pass_fds=[a.fileno()])
qemu = subprocess.Popen(split(qemu_cmd), pass_fds=[b.fileno()])
qemu.wait()
