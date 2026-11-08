#!/usr/bin/env python3
"""Footprint of `her acp` at each stage of one mock task and some seconds after it (macOS).
Shows whether the allocator gives memory back once the service goes idle.
   python3 tools/bench/mem_retention.py BINARY [runs]"""
import sys, re, pathlib, subprocess, tempfile, time, statistics
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from bench_startup import Mock, prepare
from bench_protocol_startup import Peer, task
binary = str(pathlib.Path(sys.argv[1]).resolve()); runs = int(sys.argv[2]) if len(sys.argv) > 2 else 3
mock = Mock()
def fp(pid):
    out = subprocess.run(['vmmap', '--summary', str(pid)], capture_output=True, text=True).stdout
    m = re.search(r'Physical footprint:\s+([\d.]+)([KMG])', out)
    return float(m.group(1)) * {'K': 1/1024, 'M': 1, 'G': 1024}[m.group(2)]
rows = []
for _ in range(runs):
    tmp = pathlib.Path(tempfile.mkdtemp(prefix='ret-', dir='/tmp'))
    env, proj = prepare(tmp/'c', mock.port, config='model = "mock/bench"\npermission_mode = "manual"\n')
    p = subprocess.Popen([binary, 'acp'], env=env, cwd=proj, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
    peer = Peer(p.stdout.fileno(), p.stdin.fileno(), 30)
    peer.rpc('initialize', {'protocolVersion': 1, 'clientCapabilities': {}})
    r = {'init': fp(p.pid)}
    task(peer, 'acp', proj, mock)
    time.sleep(0.3); r['task+0.3s'] = fp(p.pid)
    time.sleep(2.7); r['task+3s'] = fp(p.pid)
    time.sleep(5.0); r['task+8s'] = fp(p.pid)
    rows.append(r)
    p.stdin.close(); p.wait(timeout=10)
print('  '.join(f"{k} {statistics.median(x[k] for x in rows):.2f}" for k in rows[0]))
