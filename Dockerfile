FROM scratch

COPY ./backup-controller /backup-controller

ENTRYPOINT ["/backup-controller"]
