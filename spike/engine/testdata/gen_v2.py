# Makes self-generated v2-only and hybrid test torrents (libtorrent 2.x).
# docker run --rm -v "$PWD/spike/engine/testdata:/out" ubuntu:24.04 bash -c \
#   "apt-get update -qq && apt-get install -y -qq python3-libtorrent >/dev/null && python3 /out/gen_v2.py"
import os, random
import libtorrent as lt

root = "/out/pack"
os.makedirs(root, exist_ok=True)
rng = random.Random(42)
for name, size in (("a.bin", 300 * 1024), ("b.bin", 200 * 1024 + 123)):
    with open(os.path.join(root, name), "wb") as f:
        f.write(bytes(rng.getrandbits(8) for _ in range(size)))

for label, flags in (("v2", lt.create_torrent.v2_only), ("hybrid", 0)):
    fs = lt.file_storage()
    lt.add_files(fs, root)
    ct = lt.create_torrent(fs, 16 * 1024, flags=flags)
    lt.set_piece_hashes(ct, "/out")
    with open(f"/out/{label}.torrent", "wb") as f:
        f.write(lt.bencode(ct.generate()))
print("ok", lt.__version__)
