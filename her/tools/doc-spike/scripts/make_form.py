# A minimal valid AcroForm: three text fields and a checkbox, Helvetica, each widget with an empty /AP.
import sys
objs = {}
objs[1] = b"<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [5 0 R 6 0 R 7 0 R 8 0 R] /DA (/Helv 12 Tf 0 g) /DR << /Font << /Helv 4 0 R >> >> >> >>"
objs[2] = b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
content = b"BT /Helv 12 Tf 50 750 Td (Company) Tj 0 -40 Td (Signer) Tj 0 -40 Td (Date) Tj 0 -40 Td (Agree) Tj ET"
objs[3] = b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /Helv 4 0 R >> >> /Contents 9 0 R /Annots [5 0 R 6 0 R 7 0 R 8 0 R] >>"
objs[4] = b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
for i, (name, y) in enumerate([("company", 742), ("signer", 702), ("date", 662)]):
    objs[5 + i] = b"<< /Type /Annot /Subtype /Widget /FT /Tx /T (%s) /Rect [150 %d 400 %d] /DA (/Helv 12 Tf 0 g) /F 4 /P 3 0 R /MK << /BC [0 0 0] >> >>" % (name.encode(), y, y + 20)
objs[8] = b"<< /Type /Annot /Subtype /Widget /FT /Btn /T (agree) /Rect [150 622 168 640] /F 4 /P 3 0 R /V /Off /AS /Off /MK << /BC [0 0 0] /CA (4) >> /DA (/ZaDb 0 Tf 0 g) /AP << /N << /Yes 10 0 R /Off 11 0 R >> >> >>"
objs[9] = b"<< /Length %d >>\nstream\n%s\nendstream" % (len(content), content)
yes = b"q 0 g BT /ZaDb 14 Tf 2 3 Td (4) Tj ET Q"
objs[10] = b"<< /Type /XObject /Subtype /Form /BBox [0 0 18 18] /Resources << /Font << /ZaDb << /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >> >> >> /Length %d >>\nstream\n%s\nendstream" % (len(yes), yes)
objs[11] = b"<< /Type /XObject /Subtype /Form /BBox [0 0 18 18] /Length 0 >>\nstream\n\nendstream"
out = bytearray(b"%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
offs = {}
for n in sorted(objs):
    offs[n] = len(out)
    out += b"%d 0 obj\n" % n + objs[n] + b"\nendobj\n"
x = len(out)
out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
for n in sorted(objs):
    out += b"%010d 00000 n \n" % offs[n]
out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, x)
open(sys.argv[1], "wb").write(out)
