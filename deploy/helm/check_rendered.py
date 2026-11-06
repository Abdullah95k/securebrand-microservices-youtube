#!/usr/bin/env python3
"""Checks the rendered yts chart (REQ-010 fixes 13, 14, 17).

Usage: helm template ... | python3 deploy/helm/check_rendered.py

Fails (exit 1) when a pod could run as root or write its root filesystem,
keeps a capability, lacks requests or limits, when the worker could run
twice, when a secret file is more open than 0440, when an alert has no
runbook link, or when the network policy, the monitoring objects or the
migration hook are missing.
"""
import sys

import yaml

problems = []


def check(ok, msg):
    if not ok:
        problems.append(msg)


docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
by_kind = {}
for d in docs:
    by_kind.setdefault(d["kind"], []).append(d)

workloads = by_kind.get("Deployment", []) + by_kind.get("Job", [])
check(workloads, "no workloads rendered")
names = {w["metadata"]["name"] for w in workloads}
for want in ("-worker", "-scheduler", "-migrate"):
    check(any(n.endswith(want) for n in names), f"no {want[1:]} workload")

for w in workloads:
    name = f'{w["kind"]}/{w["metadata"]["name"]}'
    pod = w["spec"]["template"]["spec"]
    psc = pod.get("securityContext", {})
    check(psc.get("runAsNonRoot") is True, f"{name}: runAsNonRoot is not true")
    check(psc.get("runAsUser") == 65532 and psc.get("runAsGroup") == 65532, f"{name}: not user and group 65532")
    check(psc.get("fsGroup") == 65532, f"{name}: fsGroup is not 65532 (secret files would be unreadable)")
    check(pod.get("automountServiceAccountToken") is False, f"{name}: mounts a service account token")
    for vol in pod.get("volumes", []):
        if "secret" in vol:
            check(vol["secret"].get("defaultMode") == 0o440, f"{name}: secret volume {vol['name']} mode is not 0440")
    for c in pod["containers"]:
        cname = f"{name}/{c['name']}"
        sc = c.get("securityContext", {})
        check(sc.get("readOnlyRootFilesystem") is True, f"{cname}: root filesystem is writable")
        check(sc.get("allowPrivilegeEscalation") is False, f"{cname}: privilege escalation allowed")
        check(sc.get("capabilities", {}).get("drop") == ["ALL"], f"{cname}: capabilities not all dropped")
        res = c.get("resources", {})
        for part in ("requests", "limits"):
            check(res.get(part, {}).get("cpu") and res.get(part, {}).get("memory"), f"{cname}: no {part} for cpu and memory")
        check(c["command"] == ["/usr/local/bin/ytcollect"], f"{cname}: not the ytcollect binary")
        mounts = {m["mountPath"] for m in c.get("volumeMounts", [])}
        check("/tmp" in mounts, f"{cname}: no writable /tmp")
        env = {e["name"]: e.get("value") for e in c.get("env", [])}
        check("YT_POSTGRES_DIR" in env or "YT_DATABASE_URL_FILE" in env, f"{cname}: no database settings")
        if w["kind"] == "Deployment":
            check(c.get("livenessProbe") and c.get("readinessProbe"), f"{cname}: no liveness or readiness probe")
            check("YT_PRIVACY_KEY_FILE" in env, f"{cname}: no privacy key")
    if w["metadata"]["name"].endswith("-worker"):
        check(w["spec"]["replicas"] == 1, f"{name}: more than one worker would double the rate per proxy IP")
        check(w["spec"].get("strategy", {}).get("type") == "Recreate", f"{name}: not Recreate (an update would run two)")
        args = pod["containers"][0]["args"]
        check(any(a.startswith("-interval=") for a in args), f"{name}: no -interval")
        env = {e["name"] for e in pod["containers"][0].get("env", [])}
        check("YT_PROXIES_FILE" in env, f"{name}: no proxies")
    if w["kind"] == "Job":
        hook = w["metadata"].get("annotations", {}).get("helm.sh/hook", "")
        check("pre-install" in hook and "pre-upgrade" in hook, f"{name}: not a pre-install and pre-upgrade hook")

rules = by_kind.get("PrometheusRule", [])
check(len(rules) == 1, "no PrometheusRule")
for r in rules:
    alerts = [a for g in r["spec"]["groups"] for a in g["rules"] if "alert" in a]
    check(len(alerts) >= 19, f"only {len(alerts)} alerts")
    for a in alerts:
        check(a.get("annotations", {}).get("runbook_url", "").startswith("https://"), f"alert {a['alert']}: no runbook link")
check(len(by_kind.get("ServiceMonitor", [])) == 1, "no ServiceMonitor")
nps = by_kind.get("NetworkPolicy", [])
check(len(nps) == 1, "no NetworkPolicy")
for np in nps:
    check(set(np["spec"]["policyTypes"]) == {"Ingress", "Egress"}, "the NetworkPolicy must limit both directions")
    egress = np["spec"].get("egress", [])
    dns = [r for r in egress if any(p.get("port") == 53 for p in r.get("ports", []))]
    check(dns, "the NetworkPolicy must allow DNS")
    https = [r for r in egress if [p.get("port") for p in r.get("ports", [])] == [443]]
    check(https, "the NetworkPolicy must allow HTTPS")
    for r in egress:
        for p in r.get("ports", []):
            check(isinstance(p.get("port"), int) and 0 < p["port"] < 65536, f"egress port {p.get('port')!r} is not a port number")

if problems:
    print("Rendered chart problems:")
    for p in problems:
        print(" -", p)
    sys.exit(1)
print(f"Rendered chart OK: {len(docs)} objects, {len(workloads)} workloads.")
