"""Render the Stream Deck icon family using Lightwave's native visual language.

Run from any directory: python3 streamdeck/genicons.py
Requires Pillow (python3 -m pip install Pillow). All artwork is procedural;
72px and 144px PNGs and the review sheet are generated from the same geometry.
"""
from pathlib import Path
import math
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = Path(__file__).resolve().parent
OUT = ROOT / 'com.dinksf.lightwave.sdPlugin' / 'imgs'
S = 4
SIZE = 144
NEON = '#b026ff'
MAG = '#ff2ec8'
ICE = '#e9d5ff'
DIM = '#76568f'


def layer():
    return Image.new('RGBA', (SIZE*S, SIZE*S))


def line(im, points, fill=ICE, width=4):
    d = ImageDraw.Draw(im)
    points = [(round(x*S), round(y*S)) for x, y in points]
    d.line(points, fill=fill, width=round(width*S), joint='curve')
    r = width*S/2
    for x, y in (points[0], points[-1]):
        d.ellipse((x-r, y-r, x+r, y+r), fill=fill)


def box(im, bounds, fill=None, outline=None, width=2, radius=5):
    ImageDraw.Draw(im).rounded_rectangle(tuple(round(v*S) for v in bounds), radius*S,
                                        fill=fill, outline=outline, width=round(width*S))


def circle(im, x, y, r, fill=None, outline=None, width=3):
    ImageDraw.Draw(im).ellipse(((x-r)*S, (y-r)*S, (x+r)*S, (y+r)*S),
                              fill=fill, outline=outline, width=round(width*S))


def arc(im, bounds, start, end, fill=ICE, width=4):
    ImageDraw.Draw(im).arc(tuple(v*S for v in bounds), start, end, fill=fill, width=width*S)


def star(im, x, y, r, on=True):
    points = [(x, y-r), (x+r*.16, y-r*.16), (x+r*.76, y),
              (x+r*.16, y+r*.16), (x, y+r), (x-r*.16, y+r*.16),
              (x-r*.76, y), (x-r*.16, y-r*.16), (x, y-r)]
    if on:
        ImageDraw.Draw(im).polygon([(a*S, b*S) for a,b in points], fill=ICE)
        circle(im, x,y,3, '#ffffff')
    else:
        line(im, points, DIM, 2.5)


def plate(active=False):
    im = layer()
    # Dark plum glass, violet illumination, and fine scanlines from the HUD.
    pixels = im.load()
    for y in range(SIZE*S):
        for x in range(SIZE*S):
            nx, ny = x/(SIZE*S), y/(SIZE*S)
            glow = math.exp(-((nx-.5)**2/.10 + (ny-.36)**2/.12))
            strength = 1 if active else .35
            scan = 1.4 if y % (3*S) < S else 0
            pixels[x,y] = (int(10+38*glow*strength+scan), int(6+5*glow),
                           int(18+55*glow*strength+scan), 255)
    mask = Image.new('L', im.size)
    ImageDraw.Draw(mask).rounded_rectangle((3*S,3*S,141*S,141*S), 22*S, fill=255)
    im.putalpha(mask)
    box(im, (4,4,140,140), outline=NEON if active else '#4a1c66', width=1.3, radius=21)
    line(im, [(32,7),(112,7)], '#8530ad' if active else '#49205e', 1)
    return im


def glyph(kind, on=True):
    im = layer()
    ink = ICE if on else DIM
    if kind in ('pad', 'logo'):
        star(im,72,65,35,on)
    elif kind == 'alloff':
        arc(im,(45,35,99,89),-48,228,ink,5)
        line(im,[(72,29),(72,58)],ink,5)
        for x in (56,72,88):
            circle(im,x,104,3,MAG if on else DIM)
    elif kind == 'palette':
        for i,c in enumerate((NEON,MAG,'#ff7a3d')):
            x=34+i*27
            box(im,(x,37,x+22,91),fill=c,radius=7)
            line(im,[(x+6,44),(x+15,44)],'#ffffff',1.5)
    elif kind == 'dance':
        for i,c in enumerate((NEON,MAG,ICE)):
            points=[(x,52+i*14+9*math.sin((x-28)/88*math.pi*2-i*.65)) for x in range(28,117)]
            line(im,points,c if on else DIM,3.5)
        if on:
            line(im,[(64,95),(64,107)],MAG,3)
            line(im,[(80,95),(80,107)],MAG,3)
        else:
            ImageDraw.Draw(im).polygon([(67*S,94*S),(67*S,109*S),(80*S,101.5*S)],fill=ICE)
    elif kind == 'gradient':
        for i,c in enumerate((NEON,'#d82be4',MAG)):
            x=31+i*29
            box(im,(x,37,x+24,94),fill=c if on else NEON,radius=5)
            circle(im,x+12,103,2.5,c if on else NEON)
    elif kind == 'brightness':
        circle(im,72,65,17,outline=ICE,width=4)
        for i in range(8):
            a=i*math.pi/4
            line(im,[(72+26*math.cos(a),65+26*math.sin(a)),
                     (72+34*math.cos(a),65+34*math.sin(a))],MAG if i%2 else NEON,4)
    elif kind == 'sweep':
        for i,x in enumerate((34,59,84,109)):
            box(im,(x-8,73-i*9,x+8,94),fill=(DIM,NEON,'#d82be4',MAG)[i],radius=4)
        line(im,[(32,108),(111,108)],ICE,3)
        line(im,[(104,102),(111,108),(104,114)],ICE,3)
    elif kind == 'status':
        box(im,(29,31,115,98),outline=ICE,width=3,radius=9)
        line(im,[(39,68),(50,68),(59,49),(72,82),(82,60),(105,60)],MAG,3)
        for x,c in ((58,NEON),(72,MAG),(86,ICE)):
            circle(im,x,109,3,c)
    else:
        raise ValueError(kind)
    return im


def icon(kind, on=True, titled=False):
    base=plate(on)
    art=glyph(kind,on)
    # Two-line Stream Deck titles occupy the lower third of titled keys.
    if titled:
        small=art.resize((round(SIZE*S*.77),round(SIZE*S*.77)),Image.Resampling.LANCZOS)
        art=layer()
        art.alpha_composite(small,(round(16.5*S),round(1*S)))
    glow=art.filter(ImageFilter.GaussianBlur(5*S))
    glow.putalpha(glow.getchannel('A').point(lambda a: round(a*(.65 if on else .2))))
    base=Image.alpha_composite(base,glow)
    return Image.alpha_composite(base,art)


ACTIONS = [('status','Status'),('pad','Light'),('alloff','All Lights'),
           ('palette','Palette'),('dance','Color Fade'),('gradient','Pattern'),
           ('brightness','Brightness'),('sweep','Light Sweep')]


def main():
    targets={f'actions/{kind}':icon(kind) for kind,_ in ACTIONS}
    for kind in ('status','palette','brightness'):
        targets[f'actions/{kind}-key']=icon(kind)
    for name,kind in (('pad','pad'),('dance','dance'),('gradient','gradient')):
        for on in (False,True):
            targets[f'actions/{name}-{"on" if on else "off"}']=icon(kind,on,titled=True)
    targets['actions/alloff-key']=icon('alloff',False,True)
    targets['actions/allon-key']=icon('alloff',True,True)
    targets['plugin']=icon('logo')
    targets['category']=icon('logo')
    for name,im in targets.items():
        path=OUT/name
        path.parent.mkdir(parents=True,exist_ok=True)
        for suffix,size in (('',72),('@2x',144)):
            im.resize((size,size),Image.Resampling.LANCZOS).save(f'{path}{suffix}.png')

    # Review at actual key sizes, with titles to check the reserved text area.
    sheet=Image.new('RGB',(1000,550),'#0a0612')
    d=ImageDraw.Draw(sheet)
    font=ImageFont.truetype('/System/Library/Fonts/Helvetica.ttc',16) if Path('/System/Library/Fonts/Helvetica.ttc').exists() else ImageFont.load_default(size=16)
    d.text((24,18),'LIGHTWAVE / STREAM DECK',fill=ICE,font=font)
    for i,(kind,label) in enumerate(ACTIONS):
        x=24+i*122
        im=targets[f'actions/{kind}'].resize((100,100),Image.Resampling.LANCZOS)
        sheet.paste(im,(x,60),im)
        d.text((x,172),label,fill=ICE,font=font)
        im=targets[f'actions/{kind}'].resize((72,72),Image.Resampling.LANCZOS)
        sheet.paste(im,(x+14,205),im)
    d.text((24,306),'KEY STATES / TITLE SAFE AREA',fill=ICE,font=font)
    names=[('pad-off','Desk Strip'),('pad-on','Desk Strip'),('alloff-key','All Lights'),('allon-key','All Lights'),('dance-off','Start\nFade'),('dance-on','Stop\nFade'),('gradient-off','Use\nGradient'),('gradient-on','Use\nSolid')]
    for i,(name,label) in enumerate(names):
        x=24+i*122
        im=targets[f'actions/{name}'].resize((100,100),Image.Resampling.LANCZOS)
        sheet.paste(im,(x,345),im)
        d.multiline_text((x+50,409),label,fill='white',font=font,anchor='ma',align='center',spacing=0)
        d.text((x,467),'ON' if i%2 else 'OFF',fill=ICE if i%2 else DIM,font=font)
    sheet.save(ROOT.parent/'docs/img/action-icons.png')
    print(f'Rendered {len(targets)*2} PNGs and docs/img/action-icons.png')


if __name__ == '__main__':
    main()
