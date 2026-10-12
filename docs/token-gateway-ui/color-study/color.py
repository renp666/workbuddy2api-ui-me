# OKLCH <-> sRGB + WCAG 对比度计算（纯标准库实现）
import math

def oklch_to_linear(L, C, H_deg):
    h = math.radians(H_deg)
    a = C * math.cos(h)
    b = C * math.sin(h)
    l_ = L + 0.3963377774*a + 0.2158037573*b
    m_ = L - 0.1055613458*a - 0.0638541728*b
    s_ = L - 0.0894841775*a - 1.2914855480*b
    l, m, s = l_**3, m_**3, s_**3
    r =  4.0767416621*l - 3.3077115913*m + 0.2309699292*s
    g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
    bl = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
    return (r, g, bl)

def linear_to_srgb(c):
    c = max(0.0, min(1.0, c))
    return 12.92*c if c <= 0.0031308 else 1.055*(c**(1/2.4)) - 0.055

def hex_to_linear(hexstr):
    h = hexstr.lstrip('#')
    r, g, b = (int(h[i:i+2],16)/255 for i in (0,2,4))
    def s2l(c): return c/12.92 if c <= 0.04045 else ((c+0.055)/1.055)**2.4
    return (s2l(r), s2l(g), s2l(b))

def in_gamut(L, C, H):
    r,g,b = oklch_to_linear(L,C,H)
    return all(-0.0005 <= v <= 1.0005 for v in (r,g,b))

def oklch_to_hex(L, C, H):
    r,g,b = oklch_to_linear(L,C,H)
    return '#%02X%02X%02X' % tuple(int(round(linear_to_srgb(v)*255)) for v in (r,g,b))

def hex_to_oklch(hexstr):
    r,g,b = hex_to_linear(hexstr)
    l = 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
    m = 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
    s = 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
    l_ = math.copysign(abs(l)**(1/3), l)
    m_ = math.copysign(abs(m)**(1/3), m)
    s_ = math.copysign(abs(s)**(1/3), s)
    L = 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
    a = 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
    bb = 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_
    C = math.hypot(a, bb)
    H = math.degrees(math.atan2(bb, a)) % 360
    return (L, C, H)

def luminance(lin):
    return 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2]

def contrast(hex1, hex2):
    l1, l2 = luminance(hex_to_linear(hex1)), luminance(hex_to_linear(hex2))
    if l1 < l2: l1, l2 = l2, l1
    return (l1+0.05)/(l2+0.05)

def chroma_max(L, H, cap=0.4):
    lo, hi = 0.0, cap
    for _ in range(40):
        mid = (lo+hi)/2
        if in_gamut(L, mid, H): lo = mid
        else: hi = mid
    return lo

# ---- 读取项目现有四通道色 ----
CUR = {
 'workbuddy': ('#4f52d6', '#3d40a8', '#eef0fa', '#8f92ff'),
 'zcode':     ('#0a7f96', '#076b80', '#e6f4f7', '#4fd6e8'),
 'qoder':     ('#7a3fc4', '#6a36ab', '#f2ecfb', '#b78cf5'),
 'opencode':  ('#c4356f', '#b02f63', '#fbeaf1', '#ff8fb8'),
}
ACCENT = '#5b4bf0'

print('='*76)
print('项目现有「一束色」硬数据诊断')
print('='*76)
print(f"{'通道':<11}{'本色':<9}{'OKLCH':<24}{'文字版对比':<11}{'暗底版对比':<11}")
print('-'*76)
hues = []
for name,(base, ink, wash, lit) in CUR.items():
    L,C,H = hex_to_oklch(base)
    c_ink = contrast(ink, wash)
    # 暗底 #0f1120 上的对比
    c_lit = contrast(lit, '#0f1120')
    hues.append(H)
    print(f"{name:<11}{base:<9}L={L:.3f} C={C:.3f} H={H:6.1f}   {c_ink:>5.2f}:1    {c_lit:>5.2f}:1")

print('-'*76)
print('色相分布（升序）:', ' '.join(f'{h:.0f}°' for h in sorted(hues)))
gaps = []
hs = sorted(hues)
for i in range(len(hs)):
    g = hs[(i+1)%len(hs)] - hs[i]
    if g < 0: g += 360
    gaps.append(g)
print('相邻色相间距:', ' '.join(f'{g:.0f}°' for g in gaps))
print(f'最小间距: {min(gaps):.0f}°   平均间距: {sum(gaps)/len(gaps):.0f}°')
Ls = [hex_to_oklch(c[0])[0] for c in CUR.values()]
Cs = [hex_to_oklch(c[0])[1] for c in CUR.values()]
print(f'明度 L 范围: {min(Ls):.3f} ~ {max(Ls):.3f}  (差 {max(Ls)-min(Ls):.3f})')
print(f'彩度 C 范围: {min(Cs):.3f} ~ {max(Cs):.3f}  (差 {max(Cs)-min(Cs):.3f})')
