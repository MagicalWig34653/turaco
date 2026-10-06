# LAB ONLY. Tiny Samba AD DC image from a pinned Debian base (multi-arch, runs on arm64).
FROM debian:12.11-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends samba samba-ad-dc samba-ad-provision krb5-user winbind ldb-tools ldap-utils dnsutils procps ca-certificates openssl \
 && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh provision-users.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/provision-users.sh
VOLUME ["/var/lib/samba"]
EXPOSE 389 636 88 88/udp
HEALTHCHECK --interval=5s --timeout=5s --retries=40 --start-period=30s \
  CMD test -f /var/lib/samba/.lab-provisioned && ldapsearch -x -H ldap://127.0.0.1 -s base -b "" namingContexts >/dev/null
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
