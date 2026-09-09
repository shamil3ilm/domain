# step-ca — private certificate authority with an ACME provisioner.
# Caddy talks to it over ACME to obtain internal certs for *.myworld services.
FROM smallstep/step-ca:0.27.4

USER root
COPY init.sh /usr/local/bin/init-ca.sh
RUN chmod +x /usr/local/bin/init-ca.sh && chown step:step /usr/local/bin/init-ca.sh
USER step

ENTRYPOINT ["/usr/local/bin/init-ca.sh"]
