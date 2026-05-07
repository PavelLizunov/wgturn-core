# wgturn-core HANDBOOK

Operational guide. How to make changes to this project, build, test,
and deploy. Targeted at someone (human or Claude) picking up the project
in a fresh session.

## Infrastructure map

All work happens across these LAN hosts:

| Host | IP | Role | Auth |
|---|---|---|---|
| **claude container** | (no public IP) | This Claude session runs here | n/a |
| **homelab .207** | `192.168.0.207` | Forgejo + Go toolchain + handoff bundle | `ssh user@192.168.0.207` (key already authorized) |
| **homelab .236** | `192.168.0.236` | Forgejo Actions runner (Docker) | `ssh user@192.168.0.236` |
| **homelab .142** | `192.168.0.142` | E2E client testbed: headless Chrome on `:9222`, used as wgturn-cli client during real-VK tests | `ssh user@192.168.0.142` |
| **is-01** | `93.95.226.167` | wgturn-server (Iceland VPS) | `ssh user@192.168.0.207 "ssh root@93.95.226.167 ..."` (proxy through .207) |

## Forgejo (self-hosted git)

- Web UI: `http://192.168.0.207:18300`
- SSH: `ssh://git@192.168.0.207:18222`
- User: `slovn` / password `ueO1Ra4ClLResfGxNf7DyiFd` (in homelab notes)
- API: same web URL, basic auth with the credentials above
- The container's SSH key is authorized at Forgejo for both fetch + push.

To fetch CI logs:
```bash
ssh user@192.168.0.207 "docker exec forgejo find /data/gitea/actions_log/slovn/wgturn-core -name '<task-id>.log.zst'"
ssh user@192.168.0.207 "docker cp forgejo:<path> /tmp/<id>.log.zst && zstd -dc /tmp/<id>.log.zst"
```

To check task statuses:
```bash
ssh user@192.168.0.207 "curl -s -u slovn:ueO1Ra4ClLResfGxNf7DyiFd 'http://localhost:18300/api/v1/repos/slovn/wgturn-core/actions/tasks?limit=5'" | python3 -m json.tool
```

## Go toolchain

Project requires **Go 1.25**. The toolchain on .207 lives at
`~/go125/go/bin/` (the system `go` is 1.22, too old). All build commands
must `export PATH=~/go125/go/bin:$PATH`.

`golangci-lint` is at `~/go/bin/golangci-lint` — same shell, add `~/go/bin` to PATH.

## Common command bundles

### Sync local → .207 + build + test
```bash
cd /home/user/workspace/wgturn-core
tar -czf /tmp/wgturn-core.tgz --exclude=.git --exclude='*.exe' .
scp -o BatchMode=yes /tmp/wgturn-core.tgz user@192.168.0.207:/tmp/
ssh user@192.168.0.207 "rm -rf ~/wgturn-core && mkdir -p ~/wgturn-core && \
  tar -xzf /tmp/wgturn-core.tgz -C ~/wgturn-core && cd ~/wgturn-core && \
  export PATH=~/go125/go/bin:~/go/bin:\$PATH && \
  gofmt -w . && go build ./... && go vet ./... && go test -race ./... && \
  golangci-lint run ./... 2>&1 | tail -20"
```

### Pull formatted files back
```bash
scp user@192.168.0.207:~/wgturn-core/<changed-files> /home/user/workspace/wgturn-core/<path>
```

### Cross-compile bundle
```bash
ssh user@192.168.0.207 "cd ~/wgturn-core && export PATH=~/go125/go/bin:\$PATH && \
  for plat in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    os=\$(echo \$plat | cut -d/ -f1); arch=\$(echo \$plat | cut -d/ -f2)
    ext=''; [ \"\$os\" = windows ] && ext=.exe
    out=~/wgturn-handoff/wgturn-cli-\${os}-\${arch}\${ext}
    GOOS=\$os GOARCH=\$arch CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' \
      -o \$out ./cmd/wgturn-cli && ls -lah \$out | awk '{print \$5, \$NF}'
  done"
```

### Tail wgturn-server on is-01
```bash
ssh user@192.168.0.207 "ssh root@93.95.226.167 'docker logs --tail 50 wgturn-server'"
```

### Check running CI status (programmatic)
```bash
ssh user@192.168.0.207 "curl -s -u slovn:ueO1Ra4ClLResfGxNf7DyiFd \
  'http://localhost:18300/api/v1/repos/slovn/wgturn-core/actions/tasks?limit=2'" \
  | python3 -c "
import sys, json
for t in json.load(sys.stdin).get('workflow_runs', [])[:2]:
    print(t['id'], t.get('status'), t.get('head_sha','')[:8])
"
```

## End-to-end test against real VK

This burns VK API quota — VK rate-limits anonymous tokens per source IP.
Don't do more than ~3 fetches per 10 minutes from .142. After that,
wait ~30 min for the cooldown.

```bash
# 1. Ensure Chrome on .142 is up
ssh user@192.168.0.142 "curl -s http://localhost:9222/json/version | head -c 200"

# 2. Run wgturn-cli foreground
ssh user@192.168.0.142 "/tmp/wgturn-cli-linux-amd64 \
  -peer 93.95.226.167:56000 \
  -listen 127.0.0.1:9100 \
  -vk-link 'https://vk.ru/call/join/<callID>' \
  -vk-chrome-url 'http://127.0.0.1:9222' \
  -vk-chrome-ua 'Mozilla/5.0 ... Chrome/146.0.0.0 Safari/537.36' \
  -udp -v"
```

Expected log sequence: `[vk] captcha required` → `[cdp-solver] got
success_token` → `[vk] stream=N fetched: turn=...` → `[stream N]
allocation ok` → `wgturn up`.

For full WG-tunnel verification (curl through tunnel showing exit IP =
93.95.226.167) see `~/wgturn-handoff/README.md` Path B.

## Common errors and fixes

| Symptom | Cause | Fix |
|---|---|---|
| `step3 anon token after 3 captcha attempts` | VK rate-limit | Wait 30 min |
| `cdp ws dial: ... [::1]:9222: connection refused` | IPv6 vs IPv4 | Use `127.0.0.1`, not `localhost` |
| `cdp open tab: status 405` | Old Chrome (< M111) requires GET | Use Chrome ≥ 122 |
| `dial tcp 127.0.0.1:9000: connection refused` from WG | wgturn-cli not running yet | Start CLI first, then activate WG |
| `step3 captcha solver: cdp open tab: ... connection refused` | Chrome not running | Launch Chrome with `--remote-debugging-port=9222` |
| CI fails with `data.forgejo.org: i/o timeout` on actions/checkout | Transient upstream outage, ~10% rate | Push empty commit to retrigger |
| `wgturn-server unhealthy` in `docker ps` | HEALTHCHECK exec /bin/sh on scratch image | Already mitigated: container recreated with `--no-healthcheck` |

## Releasing a new build to handoff

The handoff bundle on `192.168.0.207:~/wgturn-handoff/` is what users
download. Update procedure:

1. Cross-compile (see command above).
2. Update `~/wgturn-handoff/README.md` if usage changed.
3. Verify `wg-direct.conf` and `wg-via-wgturn.conf` are still current.
4. (Optional) `tar -czf wgturn-handoff-$(date +%Y%m%d).tgz wgturn-handoff/`
   for archival.

## Cleanup discipline

- Don't leave background `wgturn-cli` processes running on .142 after a
  test — they burn VK quota over time. `pkill -f wgturn-cli` to be sure.
- Don't leave `wgtest` WG interfaces up — `sudo wg-quick down /tmp/wgtest.conf`
  or `sudo ip link del wgtest`.
- Probe scripts in `/tmp/` are session-scratch — delete after use:
  `rm -f /tmp/vk-* /tmp/probe-* /tmp/bw-* /tmp/wgtest.conf`
