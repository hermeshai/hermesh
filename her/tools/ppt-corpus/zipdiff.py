"""Which parts of a saved deck differ from the original, byte for byte.

    zipdiff.py ORIGINAL.pptx SAVED.pptx

Exits 1 when any part changed, was added or was removed.
"""
import sys
import zipfile


def main(original, saved):
    a, b = zipfile.ZipFile(original), zipfile.ZipFile(saved)
    na, nb = set(a.namelist()), set(b.namelist())
    changed = sorted(n for n in na & nb if a.read(n) != b.read(n))
    removed, added = sorted(na - nb), sorted(nb - na)
    print(f"parts original={len(na)} saved={len(nb)} identical={len(na & nb) - len(changed)} "
          f"changed={len(changed)} removed={removed} added={added}")
    for n in changed:
        print(f"  changed: {n} {len(a.read(n))} -> {len(b.read(n))}")
    return 1 if changed or removed or added else 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    sys.exit(main(sys.argv[1], sys.argv[2]))
