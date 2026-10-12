import math
exec(open('color.py',encoding='utf-8').read().split('# ---- 读取项目现有四通道色 ----')[0])

def build(name, hues, targetL=0.55, C=None, dark_lit_L=0.78):
    """按给定色相生成一套完整的四通道色：base / ink / wash / lit"""
    out={}
    for i,(ch,H) in enumerate(zip(['workbuddy','zcode','qoder','opencode'], hues)):
        cmax = chroma_max(targetL, H)
        c = min(C[i], cmax*0.97) if C else cmax*0.92
        base = oklch_to_hex(targetL, c, H)
        # ink: 压暗到 L≈0.42，且不超过该色相最大彩度
        c_ink = min(cmax*0.95, c)
        ink_L = 0.42
        while c_ink > 0.01 and contrast(oklch_to_hex(ink_L,c_ink,H), '#ffffff') < 7.0:
            ink_L -= 0.01
        ink = oklch_to_hex(ink_L, min(c_ink, chroma_max(ink_L,H)*0.95), H)
        # wash: 极浅底 L≈0.965
        wash = oklch_to_hex(0.965, min(0.028, chroma_max(0.965,H)*0.5), H)
        # lit: 暗底文字版
        lit_c = min(chroma_max(dark_lit_L,H)*0.85, c)
        lit = oklch_to_hex(dark_lit_L, lit_c, H)
        out[ch]=dict(base=base,ink=ink,wash=wash,lit=lit,H=H,C=c)
    return out

def report(title, s):
    print('\n'+'='*84)
    print(title)
    print('='*84)
    print(f"{'通道':<11}{'本色':<9}{'OKLCH':<26}{'文字/浅底':<11}{'暗底版':<9}{'评级'}")
    print('-'*84)
    ok=True
    for ch,d in s.items():
        ci=contrast(d['ink'],d['wash']); cl=contrast(d['lit'],'#0f1120')
        grade='AA' if ci>=4.5 and cl>=4.5 else ('AA-large' if ci>=3 and cl>=3 else 'FAIL')
        if 'FAIL' in grade: ok=False
        print(f"{ch:<11}{d['base']:<9}L={hex_to_oklch(d['base'])[0]:.3f} C={d['C']:.3f} H={d['H']:6.1f}  "
              f"{ci:>5.2f}:1    {cl:>5.2f}:1  {grade}")
    hs=sorted(d['H'] for d in s.values())
    gaps=[]
    for i in range(len(hs)):
        g=hs[(i+1)%len(hs)]-hs[i]
        if g<0: g+=360
        gaps.append(g)
    print('-'*84)
    print(f"色相: {' '.join(f'{h:.0f}°' for h in hs)}   间距: {' '.join(f'{g:.0f}°' for g in gaps)}")
    print(f"最小间距 {min(gaps):.0f}°  |  " + ('✅ 全部 AA 达标' if ok else '⚠️ 有不达标项'))
    return s

# ============ 方案 A：当前结构微调（拉开 276/300 的撞色）============
A = build('A', [272, 215, 320, 358])

# ============ 方案 B：色相均匀四分（经典 90° 间隔）============
B = build('B', [255, 195, 315, 30])

# ============ 方案 C：低彩度高级灰（克制、耐看）============
C_ = build('C', [268, 210, 330, 25], targetL=0.58, C=[0.13,0.10,0.13,0.12])

# ============ 方案 D：明显区分 + 高彩度（数据界面最易辨）============
D = build('D', [255, 190, 320, 45], targetL=0.54)

for t,s in [('方案 A · 微调（保结构，拉开紫/品红）',A),
            ('方案 B · 均匀四分（蓝/青/紫/橙）',B),
            ('方案 C · 低彩度灰调（克制耐看）',C_),
            ('方案 D · 高区分（蓝/青/紫/琥珀）',D)]:
    report(t,s)

import json
json.dump({k:v for k,v in [('A',A),('B',B),('C',C_),('D',D)]},
          open('schemes.json','w',encoding='utf-8'), ensure_ascii=False, indent=1)
print('\n已保存 schemes.json')
