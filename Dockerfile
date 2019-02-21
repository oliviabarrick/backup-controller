FROM scratch

COPY ./snapshot-webhook /snapshot-webhook

ENTRYPOINT ["/snapshot-webhook"]
