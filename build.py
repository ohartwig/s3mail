"""Baut aus s3mail_core.py + s3mail_web.py die eine Datei s3mail.py."""
import re, pathlib

core = pathlib.Path("s3mail_core.py").read_text(encoding="utf-8")
setup = pathlib.Path("s3mail_setup.py").read_text(encoding="utf-8")
web = pathlib.Path("s3mail_web.py").read_text(encoding="utf-8")

# Core: Shebang + Modul-Docstring abtrennen
core_body = core.split('"""', 2)[2].lstrip("\n")
core_body = core_body.replace("from __future__ import annotations\n", "", 1).lstrip("\n")

def split(text):
    pre, _, post = text.partition("# === BUILD:SPLIT ===")
    imports = "\n".join(
        l for l in pre.splitlines()
        if l.startswith(("import ", "from ")) and "s3mail_core" not in l
        and "s3mail_setup" not in l and not l.startswith("from __future__"))
    return imports, post.lstrip("\n")

setup_imports, setup_body = split(setup)
web_imports, post = split(web)

header = '''#!/usr/bin/env python3
"""
s3mail - Mail-Client fuer E-Mails, die Amazon SES in einen S3-Bucket legt.

Lokaler Webserver (nur 127.0.0.1), Postfach im Browser: Ordner als echte
S3-Prefixe, Papierkorb, Spam, Tags, gelesen/ungelesen, Stern, Massenaktionen,
Auto-Regeln, Antworten/Weiterleiten ueber SES.

    pip install boto3
    python3 s3mail.py --bucket mein-mail-bucket --prefix mail/ \\
        --region eu-central-1 --from support@example.com

Benoetigte IAM-Rechte:
    s3:ListBucket    s3:GetObject    s3:PutObject    s3:DeleteObject
    ses:SendRawEmail (nur fuer Antworten/Weiterleiten)

Erzeugt aus s3mail_core.py + s3mail_web.py - nicht direkt editieren.
"""

from __future__ import annotations

'''

out = (header + setup_imports + "\n" + web_imports + "\n" + core_body
       + "\n\n" + setup_body + "\n\n" + post)
pathlib.Path("s3mail.py").write_text(out, encoding="utf-8")
print("s3mail.py gebaut:", len(out.splitlines()), "Zeilen")
