# Multi-model rollout

The proxy loads configuration once at startup. Existing scalar `model_alias`
configurations are safe to keep: they continue to expose the legacy `gonka`
route. Add `model_routes` only after the Provider aliases and route order have
been reviewed.

## Safe migration

1. Keep the real configuration in the ignored local `config.yaml` (or an
   ignored `config-*.yaml`), with file mode `0600`. Never put API keys in
   `config.example.yaml`, Compose files, or Git commits.
2. Back up the current configuration outside the repository before editing:

   ```sh
   install -m 600 config.yaml "config.yaml.$(date +%Y%m%d-%H%M%S).bak"
   ```

3. Add `model_aliases` to the relevant Gonka Providers and then add
   `model_routes`. Keep the existing scalar `model_alias` until the new routes
   have been verified; when both forms are present, `model_aliases` is used for
   a route.
4. Build and test the binary before replacing the running service:

   ```sh
   go test ./...
   go vet ./...
   go build -o gonka-proxy ./cmd/gonka-proxy
   ```

5. Restart the native service and verify it before changing Goose:

   ```sh
   sudo systemctl restart gonka-proxy
   sudo systemctl is-active --quiet gonka-proxy
   curl --fail http://127.0.0.1:58081/v1/models
   curl --fail http://127.0.0.1:58081/health
   ```

   The systemd unit reads `/home/scanum/gonka-proxy/config.yaml`; adapt the
   path if the service is installed elsewhere.

6. Send one non-sensitive smoke request for each configured Virtual Model and
   inspect `/metrics`, `/health`, and `/diagnostics`. If validation or smoke
   checks fail, restore the backup and restart the service again.

## Docker rollout

Keep the credentials-only file on the host and mount it read-only. Validate
the new configuration with the same test suite, then recreate the container:

```sh
docker compose config >/dev/null
docker compose up -d --build
curl --fail http://127.0.0.1:58081/v1/models
```

The repository's `.gitignore` excludes `config.yaml`, `config-*.yaml`, and
`compose.yaml`; check `git status` before committing any rollout changes.
