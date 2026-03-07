FROM gcr.io/distroless/static-debian12:nonroot
COPY compactor /usr/local/bin/compactor
ENTRYPOINT ["compactor"]
