# A stand-in for an Oracle Cloud Ampere (arm64) VM: Ubuntu 24.04 with systemd as PID 1, sudo, and
# the same default firewall Oracle's Ubuntu image ships (inbound rejected except SSH).
# iptables-legacy because Docker Desktop's kernel has no nftables under emulation; same rules and commands.
FROM ubuntu:24.04
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv dbus curl ca-certificates openssl sudo iptables iptables-persistent >/dev/null \
    && rm -rf /var/lib/apt/lists/* \
    && echo "ubuntu ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/ubuntu \
    && printf '%s\n' '*filter' ':INPUT ACCEPT [0:0]' ':FORWARD ACCEPT [0:0]' ':OUTPUT ACCEPT [0:0]' \
       '-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT' '-A INPUT -p icmp -j ACCEPT' '-A INPUT -i lo -j ACCEPT' \
       '-A INPUT -p tcp -m state --state NEW -m tcp --dport 22 -j ACCEPT' '-A INPUT -j REJECT --reject-with icmp-host-prohibited' \
       'COMMIT' > /etc/iptables/rules.v4
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
