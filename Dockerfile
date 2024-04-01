FROM golang:alpine AS builder
ENV USER=appuser
ENV UID=10001 
RUN apk --no-cache add ca-certificates
RUN adduser \    
    --disabled-password \    
    --gecos "" \    
    --home "/nonexistent" \    
    --shell "/sbin/nologin" \    
    --no-create-home \    
    --uid "${UID}" \    
    "${USER}"

FROM scratch
# Copy our static executable.
COPY poke_bot /go/bin/poke_bot
# Import the user and group files from the builder.
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Run the hello binary.
USER appuser:appuser
ENTRYPOINT ["/go/bin/poke_bot"]