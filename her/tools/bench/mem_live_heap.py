#!/usr/bin/env python3
"""Live malloc allocations of an idle `her acp` (after initialize), grouped by the her frames
that made them. macOS. MallocStackLogging keeps the live allocations; it inflates the footprint, so read
sizes per site here and totals from a run without it.
   python3 tools/bench/mem_live_heap.py BINARY [after-session]"""
import sys, os, re, pathlib, subprocess, tempfile, time, collections
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import mem_real
binary = str(pathlib.Path(sys.argv[1]).resolve())
stage = sys.argv[2] if len(sys.argv) > 2 else "initialize"
root = pathlib.Path(tempfile.mkdtemp(prefix='live-heap-', dir='/tmp'))
(root/'proj/.git').mkdir(parents=True); (root/'proj/.git/HEAD').write_text('ref: refs/heads/main\n'); (root/'proj/AGENTS.md').write_text('x\n')
(root/'home').mkdir(); (root/'fh').mkdir(); (root/'fh/config.toml').write_text('model = "deepseek/deepseek-flash"\n')
env = dict(PATH='/usr/bin:/bin', HOME=str(root/'home'), HER_HOME=str(root/'fh'), HER_CREDENTIAL_STORE='file', MallocStackLogging='1', DEEPSEEK_API_KEY='unused')
p = subprocess.Popen([binary, 'acp'], env=env, cwd=root/'proj', stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
acp = mem_real.Acp.__new__(mem_real.Acp); acp.p, acp.buf, acp.seq = p, b'', 0
acp.reply(acp.send('initialize', {'protocolVersion': 1, 'clientCapabilities': {}}), [])
if stage == "after-session":
    acp.reply(acp.send('session/new', {'cwd': str(root/'proj'), 'mcpServers': []}), [])
time.sleep(0.3)
out = subprocess.run(['malloc_history', str(p.pid), '-allBySize'], capture_output=True, text=True).stdout
load = re.search(r'Load Address:\s+(0x[0-9a-f]+)', out).group(1)
entries = []
for line in out.splitlines():
    m = re.match(r'(\d+) calls? for (\d+) bytes: (.*)', line)
    if not m or any(k in line for k in ('mmap', 'vm_allocate', '_tlv_get_addr', 'dyld4::')) or int(m.group(2)) >= 1 << 20:
        continue
    entries.append((int(m.group(2)), int(m.group(1)), m.group(3)))
addrs = sorted({a for _, _, s in entries for a in re.findall(r'(0x[0-9a-f]+) \(her', s)})
syms = {}
for i in range(0, len(addrs), 200):
    chunk = addrs[i:i+200]
    res = subprocess.run(['atos', '-o', binary, '-l', load] + chunk, capture_output=True, text=True).stdout.splitlines()
    for a, s in zip(chunk, res):
        syms[a] = re.sub(r'::h[0-9a-f]{16}', '', re.sub(r'\s*\(in [^)]*\)', '', s))
skip = re.compile(r'(alloc::|core::|std::|hashbrown|__rust|RawVec|serde_json::|serde::|serde_core|futures_util|tokio::runtime::task|_R[A-Za-z]*5alloc|_R[A-Za-z]*4core|_R[A-Za-z]*3std|9hashbrown|10serde_json|5serde)')
def name(s):
    m = re.findall(r'\d+([a-z_][a-z0-9_]{2,})', s)
    m = [x for x in m if x not in ('her', 'new', 'call', 'poll', 'run')]
    return '::'.join(m[-3:]) if m else s[:60]
agg = collections.Counter(); cnt = collections.Counter()
for b, n, s in entries:
    names = [name(syms.get(a, '?')) for a in re.findall(r'(0x[0-9a-f]+) \(her', s) if not skip.search(syms.get(a, '?'))]
    key = ' < '.join(reversed(names[-2:])) if names else '(system library)'
    agg[key] += b; cnt[key] += n
print(f"live malloc bytes ({stage}):", sum(b for b, _, _ in entries))
for k, v in agg.most_common(22):
    print(f"{v:8d} B {cnt[k]:4d}x  {k[:200]}")
p.stdin.close(); p.wait(timeout=10)
