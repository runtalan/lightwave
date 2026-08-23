import zlib, struct, math, os

NEON=(0xb0,0x26,0xff); MAG=(0xff,0x2e,0xc8); BG=(0x0d,0x08,0x16)
SS=3  # supersample factor -> smooth edges

def mix(a,b,t):
    t=max(0.0,min(1.0,t))
    return tuple(a[i]+(b[i]-a[i])*t for i in range(3))

def over(dst, src, a):
    return tuple(src[i]*a + dst[i]*(1-a) for i in range(3))

def smooth(edge0, edge1, x):
    if edge0 == edge1: return 0.0 if x < edge0 else 1.0
    t = max(0.0, min(1.0, (x-edge0)/(edge1-edge0)))
    return t*t*(3-2*t)

def rounded_box(nx, ny, half, r):
    """signed distance to a rounded square centred at 0, half-extent `half`."""
    qx, qy = abs(nx)-(half-r), abs(ny)-(half-r)
    ax, ay = max(qx,0.0), max(qy,0.0)
    return math.hypot(ax,ay) + min(max(qx,qy),0.0) - r

def grid_bg(nx, ny, col, strength=1.0):
    """Purple grid plate: rounded tile, subtle vertical sheen, thin gridlines."""
    d = rounded_box(nx, ny, 0.92, 0.34)
    if d > 0.02: return None, 0.0
    plate_a = smooth(0.02, -0.02, d)
    # base plate, slightly brighter at the top
    base = mix((0.16*255,0.09*255,0.28*255), (0.07*255,0.04*255,0.13*255), (ny+1)/2)
    c = base
    # gridlines
    cell = 0.46
    for axis in (nx, ny):
        f = abs(((axis/cell) % 1.0) - 0.5) * cell   # distance to nearest line
        line = smooth(0.028, 0.006, f)
        if line > 0:
            c = over(c, mix(NEON, MAG, (nx+1)/2), 0.30*line*strength)
    # inner border glow
    edge = smooth(-0.13, -0.02, d)
    if edge > 0:
        c = over(c, mix(NEON, MAG, (nx+1)/2), 0.42*edge)
    return c, plate_a

def spark(nx, ny, t=1.0):
    """Four-point spark. Returns (colour, alpha)."""
    col = mix(MAG, NEON, (ny+1)/2)
    d = math.hypot(nx, ny)
    ang = math.atan2(ny, nx)
    # star: radius modulated by 4-fold symmetry
    k = abs(math.cos(2*ang))
    rad = 0.16 + 0.40*(k**2.2)
    a = smooth(rad, rad*0.45, d) * t
    # hot core
    core = smooth(0.19, 0.0, d)
    c = mix(col, (255,255,255), core*0.85)
    # soft bloom
    bloom = smooth(0.72, 0.10, d)*0.5*t
    return c, min(1.0, a + bloom*0.55)

def render(path, size, fn):
    w=h=size
    rows=[]
    for py in range(h):
        row=bytearray()
        for px in range(w):
            racc=gacc=bacc=aacc=0.0
            for sy in range(SS):
                for sx in range(SS):
                    x=(px+(sx+0.5)/SS)/w*2-1
                    y=(py+(sy+0.5)/SS)/h*2-1
                    c,a = fn(x,y)
                    if a<=0: continue
                    racc+=c[0]*a; gacc+=c[1]*a; bacc+=c[2]*a; aacc+=a
            n=SS*SS
            if aacc<=0:
                row += bytes((0,0,0,0))
            else:
                a=aacc/n
                row += bytes((int(max(0,min(255,racc/aacc))), int(max(0,min(255,gacc/aacc))),
                              int(max(0,min(255,bacc/aacc))), int(max(0,min(255,a*255)))))
        rows.append(bytes(row))
    raw=b''.join(b'\x00'+r for r in rows)
    def chunk(t,d):
        return struct.pack('>I',len(d))+t+d+struct.pack('>I',zlib.crc32(t+d)&0xffffffff)
    open(path,'wb').write(b'\x89PNG\r\n\x1a\n'
        +chunk(b'IHDR',struct.pack('>IIBBBBB',w,h,8,6,0,0,0))
        +chunk(b'IDAT',zlib.compress(raw,9))+chunk(b'IEND',b''))

# ---- the light key: grid plate always, spark only when on ----
def pad(on):
    def f(nx,ny):
        c,a = grid_bg(nx,ny,None, 1.0 if on else 0.72)
        if a<=0: return (0,0,0),0.0
        if on:
            sc,sa = spark(nx*1.5, ny*1.5)
            if sa>0: c = over(c, sc, sa)
        else:
            # Unlit: the spark's silhouette drawn as a thin outline, so the key
            # reads as the same object switched off rather than a blank plate.
            col = mix(NEON,MAG,(nx+1)/2)
            d=math.hypot(nx*1.5, ny*1.5)
            ang=math.atan2(ny,nx); k=abs(math.cos(2*ang))
            rad=0.16+0.40*(k**2.2)
            band = smooth(0.10,0.03,abs(d-rad))
            if band>0: c = over(c, mix(col,(0,0,0),0.25), 0.62*band)
        return c,a
    return f

def power(nx,ny):
    c,a = grid_bg(nx,ny,None,0.8)
    if a<=0: return (0,0,0),0.0
    col = mix(NEON,MAG,(nx+1)/2)
    d=math.hypot(nx,ny)
    ring = smooth(0.62,0.56,d)*smooth(0.40,0.46,d)
    gap = smooth(0.34,0.26,abs(nx)) if ny<-0.20 else 0.0
    ring = ring*(1-gap)
    stem = smooth(0.10,0.06,abs(nx))*smooth(0.06,0.0,max(0,ny-0.02))*smooth(-0.70,-0.64,ny)
    m = max(ring, stem)
    if m>0: c = over(c, mix(col,(255,255,255),0.25*m), 0.95*m)
    return c,a

def palette(nx,ny):
    c,a = grid_bg(nx,ny,None,0.8)
    if a<=0: return (0,0,0),0.0
    for i in range(3):
        off = -0.34 + i*0.34
        yy = off + 0.16*math.sin(nx*3.0 + i*0.9)
        band = smooth(0.075,0.02,abs(ny-yy))
        if band>0:
            col = mix(NEON, MAG, (i/2.0)*0.85 + 0.1)
            c = over(c, col, 0.95*band)
    return c,a

def dance(on):
    def f(nx,ny):
        c,a = grid_bg(nx,ny,None, 1.0 if on else 0.72)
        if a<=0: return (0,0,0),0.0
        # three sparks in a gentle arc, the middle one large
        pts = ((-0.46,0.20,0.62),(0.0,-0.06,1.0),(0.46,0.24,0.62))
        for (cx,cy,s) in pts:
            sc,sa = spark((nx-cx)/s*1.8, (ny-cy)/s*1.8, 1.0 if on else 0.42)
            if sa>0: c = over(c, sc, sa)
        return c,a
    return f

def brightness(nx,ny):
    c,a = grid_bg(nx,ny,None,0.8)
    if a<=0: return (0,0,0),0.0
    col = mix(NEON,MAG,(nx+1)/2)
    # Three ascending bars read as "level" at 72px far better than a sun with
    # rays, which turns to mush once the key is scaled down.
    for i,(bx,bh) in enumerate(((-0.42,0.20),(0.0,0.36),(0.42,0.54))):
        inb = smooth(0.155,0.115,abs(nx-bx)) * smooth(0.02,-0.03, ny-0.46) * smooth(-0.02,0.03, ny-(0.46-2*bh))
        if inb>0:
            shade = mix(col,(255,255,255),0.10+0.18*i)
            c = over(c, shade, 0.94*inb)
    return c,a

def logo(nx,ny):
    c,a = grid_bg(nx,ny,None,1.0)
    if a<=0: return (0,0,0),0.0
    sc,sa = spark(nx*1.35, ny*1.35)
    if sa>0: c = over(c, sc, sa)
    return c,a

targets=[("actions/pad-off",pad(False)),("actions/pad-on",pad(True)),("actions/pad",pad(True)),
 ("actions/alloff",power),("actions/alloff-key",power),
 ("actions/palette",palette),("actions/palette-key",palette),
 ("actions/dance",dance(True)),("actions/dance-off",dance(False)),("actions/dance-on",dance(True)),
 ("actions/brightness",brightness),("actions/brightness-key",brightness),
 ("plugin",logo),("category",logo)]
os.makedirs("actions",exist_ok=True)
for name,fn in targets:
    render(f"{name}.png",72,fn); render(f"{name}@2x.png",144,fn)
print("rendered",len(targets)*2,"icons at",SS,"x supersampling")
