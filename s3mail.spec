# -*- mode: python ; coding: utf-8 -*-
"""
PyInstaller-Rezept fuer ein Binary, das ohne Python-Installation laeuft.

    python3 build.py                        # s3mail.py aktualisieren
    pyinstaller --clean s3mail.spec         # -> dist/s3mail/  (Ordner, schnell)
    S3MAIL_ONEFILE=1 pyinstaller --clean s3mail.spec   # -> dist/s3mail (eine Datei)

Standard ist der Ordner, und zwar aus Startzeit-Gruenden: die Einzeldatei packt
sich bei *jedem* Start in ein temporaeres Verzeichnis aus, und macOS laesst den
frisch ausgepackten Inhalt jedes Mal von XProtect pruefen - gemessen rund 7
Sekunden pro Start gegenueber einer halben Sekunde beim Ordner. Zum Weitergeben
den Ordner zippen.

Gebaut wird immer nur fuer die Plattform, auf der PyInstaller laeuft. Ein
macOS-arm64-Binary laeuft weder unter Windows noch auf einem Intel-Mac; fuer
jede Zielplattform einmal bauen.

botocore bringt die Beschreibungen *aller* AWS-Dienste mit (~26 MB). s3mail
spricht mit vieren davon, der Rest fliegt unten raus - spart etwa 20 MB.
"""

import os

ONEFILE = os.environ.get("S3MAIL_ONEFILE") == "1"

SERVICES = {"s3", "ses", "kms", "sts"}
LOOSE = {"endpoints.json", "endpoints.json.gz", "partitions.json", "_retry.json",
         "sdk-default-configuration.json"}


def keep(dest):
    """True, wenn die Datei ins Paket soll. Trifft nur botocore/boto3-Daten."""
    parts = dest.replace("\\", "/").split("/")
    if not parts or parts[0] not in ("botocore", "boto3") or "data" not in parts:
        return True
    rest = parts[parts.index("data") + 1:]
    return not rest or rest[0] in LOOSE or rest[0] in SERVICES


a = Analysis(
    ["s3mail.py"],
    pathex=[],
    binaries=[],
    datas=[],
    hiddenimports=[],
    excludes=[
        "tkinter", "unittest", "pydoc_data", "test", "lib2to3",
        "distutils", "setuptools", "pip", "pkg_resources",
        "numpy", "pandas", "matplotlib", "IPython",
    ],
    noarchive=False,
)
a.datas = [d for d in a.datas if keep(d[0])]

pyz = PYZ(a.pure)

if ONEFILE:
    exe = EXE(pyz, a.scripts, a.binaries, a.datas, [], name="s3mail",
              debug=False, strip=False, upx=True, console=True)
else:
    exe = EXE(pyz, a.scripts, [], exclude_binaries=True, name="s3mail",
              debug=False, strip=False, upx=True, console=True)
    coll = COLLECT(exe, a.binaries, a.datas, strip=False, upx=True, name="s3mail")
