"""
Master generator for 4 creative blending logotypes for Rävn.
Calibrated for:
1. Exact uniform stroke width (32px perpendicular).
2. True typographic / geometric blending (no floating stubs, no accidental clips).
3. Vibrant brand colors (International Signal Vermilion, Solar Amber, Hyper Cyan, Polar White).
4. Name ONLY (no subtitles, no framing rects, no telemetry).
5. 100% transparent background.
"""
import os

SVG_DIR = "design-logos"
os.makedirs(SVG_DIR, exist_ok=True)

# =============================================================================
# CONCEPT 1: ARCHITECTURAL CANTILEVER LIGATURE (logo-blend-1-interlock.svg)
# =============================================================================
def generate_blend_1():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Architectural Cantilever Ligature">
  <title>Rävn — Architectural Cantilever Ligature</title>
  <defs>
    <!-- International Signal Vermilion to Solar Flame -->
    <linearGradient id="b1_flame" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="65%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FF9500"/>
    </linearGradient>

    <!-- Polar White Titanium Body -->
    <linearGradient id="b1_white" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="b1_glow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.45"/>
    </filter>
  </defs>

  <g id="wordmark-ligature" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- 1. CANTILEVER GIRDER BEAM (R into Ä)                          -->
    <!-- A solid 30px Signal Vermilion beam uniting R waist to Ä       -->
    <!-- ============================================================= -->
    <rect x="62" y="118" width="186" height="30" rx="2" fill="url(#b1_flame)" filter="url(#b1_glow)"/>

    <!-- ============================================================= -->
    <!-- 2. STRUCTURAL FUSION JOINTS                                   -->
    <!-- ============================================================= -->
    <!-- Ä-to-V Bedrock Foundation Weld -->
    <polygon points="270,200 304,200 326,144 292,144" fill="url(#b1_flame)" filter="url(#b1_glow)"/>

    <!-- V-to-N Top Shoulder Crown -->
    <rect x="424" y="42" width="22" height="36" rx="2" fill="url(#b1_flame)" filter="url(#b1_glow)"/>

    <!-- ============================================================= -->
    <!-- 3. WHITE MONOLITHIC STRUCTURAL BODIES                         -->
    <!-- Strictly uniform 32px stroke width                            -->
    <!-- ============================================================= -->
    <g fill="url(#b1_white)">
      <!-- LETTER R: Stem, Bowl, Leg -->
      <rect x="30" y="42" width="32" height="158" rx="2"/>
      <path d="
        M 62,42
        L 106,42
        C 138,42 138,124 106,124
        L 62,124
        Z
        M 62,74
        L 102,74
        C 112,74 112,92 102,92
        L 62,92
        Z
      " fill-rule="evenodd"/>
      <!-- Clean angled pillar supporting the cantilever girder -->
      <polygon points="86,148 144,200 108,200 62,148"/>

      <!-- LETTER Ä: Diagonals -->
      <polygon points="
        220,42
        250,42
        304,200
        270,200
        235,96
        200,200
        166,200
      "/>

      <!-- LETTER V: Diagonals -->
      <polygon points="
        290,42
        324,42
        362,154
        400,42
        434,42
        379,200
        345,200
      "/>

      <!-- LETTER N: Left Pillar, Diagonal, Right Pillar -->
      <polygon points="
        434,42
        468,42
        522,148
        522,42
        554,42
        554,200
        520,200
        468,96
        468,200
        434,200
      "/>
    </g>

    <!-- ============================================================= -->
    <!-- 4. UMLAUT: Twin Signal Vermilion Discs                        -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#b1_flame)" filter="url(#b1_glow)"/>
    <circle cx="252" cy="18" r="14" fill="url(#b1_flame)" filter="url(#b1_glow)"/>

  </g>
</svg>'''

# =============================================================================
# CONCEPT 2: CONTINUOUS KINETIC RIBBON (logo-blend-2-ribbon.svg)
# =============================================================================
def generate_blend_2():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Kinetic Ribbon Wave">
  <title>Rävn — Kinetic Ribbon Wave</title>
  <defs>
    <!-- Fluid Kinetic Plumage Spectrum -->
    <linearGradient id="b2_spectrum" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="18%" stop-color="#C7D2FE"/>
      <stop offset="38%" stop-color="#8B5CF6"/>
      <stop offset="60%" stop-color="#FF3B30"/>
      <stop offset="82%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- 3D Fold Shading -->
    <linearGradient id="b2_fold_shade" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#000000" stop-opacity="0.35"/>
      <stop offset="100%" stop-color="#000000" stop-opacity="0.0"/>
    </linearGradient>

    <linearGradient id="b2_amber_gold" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <filter id="b2_aura" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="5" stdDeviation="7" flood-color="#FF3B30" flood-opacity="0.35"/>
    </filter>
  </defs>

  <g id="wordmark-ribbon" filter="url(#b2_aura)" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- R STEM & BOWL                                                 -->
    <!-- ============================================================= -->
    <rect x="30" y="42" width="32" height="158" rx="2" fill="url(#b2_spectrum)"/>
    <path d="
      M 62,42
      L 106,42
      C 138,42 138,124 106,124
      L 62,124
      Z
      M 62,74
      L 102,74
      C 112,74 112,92 102,92
      L 62,92
      Z
    " fill="url(#b2_spectrum)" fill-rule="evenodd"/>

    <!-- ============================================================= -->
    <!-- R LEG SWEEPING ACROSS BASELINE INTO Ä LEFT DIAGONAL           -->
    <!-- Clean geometric continuous ribbon sweep                       -->
    <!-- ============================================================= -->
    <polygon points="
      88,122
      138,200
      172,200
      220,42
      252,42
      202,200
      168,200
      118,122
    " fill="url(#b2_spectrum)"/>

    <!-- Fold shadow where R leg tucks behind Ä -->
    <polygon points="118,122 138,200 122,200 94,122" fill="url(#b2_fold_shade)"/>

    <!-- ============================================================= -->
    <!-- Ä CROSSBAR & RIGHT DIAGONAL                                   -->
    <!-- ============================================================= -->
    <!-- Ä Crossbar Bridge (strictly flush inside Ä diagonals) -->
    <polygon points="193,122 247,122 257,152 183,152" fill="url(#b2_spectrum)"/>

    <!-- Ä Right Diagonal descending and looping into V Left Arm!       -->
    <polygon points="
      220,42
      252,42
      304,200
      338,42
      370,42
      322,200
      288,200
      242,96
      220,42
    " fill="url(#b2_spectrum)"/>

    <!-- Fold shadow between Ä right foot and V left arm -->
    <polygon points="288,200 304,200 326,144 314,144" fill="url(#b2_fold_shade)"/>

    <!-- ============================================================= -->
    <!-- V VERTEX & RIGHT ARM ASCENDING TO N                           -->
    <!-- ============================================================= -->
    <polygon points="
      322,200
      356,200
      400,42
      434,42
      379,200
      345,200
    " fill="url(#b2_spectrum)"/>

    <!-- ============================================================= -->
    <!-- LETTER N                                                      -->
    <!-- Left Pillar, Cascading Diagonal, Right Pillar                 -->
    <!-- ============================================================= -->
    <polygon points="
      434,42
      468,42
      522,148
      522,42
      554,42
      554,200
      520,200
      468,96
      468,200
      434,200
    " fill="url(#b2_spectrum)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Glowing Solar Discs                              -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#b2_amber_gold)"/>
    <circle cx="252" cy="18" r="14" fill="url(#b2_amber_gold)"/>

  </g>
</svg>'''

# =============================================================================
# CONCEPT 3: CHROMATIC PRISM FUSION (logo-blend-3-chromatic.svg)
# =============================================================================
def generate_blend_3():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Chromatic Prism Fusion">
  <title>Rävn — Chromatic Prism Fusion</title>
  <defs>
    <!-- Facet 1: International Signal Vermilion -->
    <linearGradient id="b3_vermilion" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#FF2D55"/>
    </linearGradient>

    <!-- Facet 2: Solar Amber Gold -->
    <linearGradient id="b3_amber" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- Facet 3: Hyper Cyan -->
    <linearGradient id="b3_cyan" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#00F0FF"/>
      <stop offset="100%" stop-color="#007AFF"/>
    </linearGradient>

    <!-- Titanium Polar White Body -->
    <linearGradient id="b3_white" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="b3_vermilion_bloom" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.45"/>
    </filter>
    <filter id="b3_cyan_bloom" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#00F0FF" flood-opacity="0.45"/>
    </filter>
  </defs>

  <g id="wordmark-chromatic" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- WHITE MONOLITHIC BODIES (R, Ä, V, N)                          -->
    <!-- ============================================================= -->
    <g fill="url(#b3_white)">
      <!-- LETTER R -->
      <rect x="30" y="42" width="32" height="158" rx="2"/>
      <path d="
        M 62,42
        L 106,42
        C 138,42 138,124 106,124
        L 62,124
        Z
        M 62,74
        L 102,74
        C 112,74 112,92 102,92
        L 62,92
        Z
      " fill-rule="evenodd"/>
      <polygon points="90,122 144,200 108,200 62,132"/>

      <!-- LETTER Ä -->
      <polygon points="
        220,42
        250,42
        304,200
        270,200
        235,96
        200,200
        166,200
      "/>

      <!-- LETTER V -->
      <polygon points="
        290,42
        324,42
        362,154
        400,42
        434,42
        379,200
        345,200
      "/>

      <!-- LETTER N -->
      <polygon points="
        434,42
        468,42
        522,148
        522,42
        554,42
        554,200
        520,200
        468,96
        468,200
        434,200
      "/>
    </g>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 1: R ∩ Ä FUSION (Signal Vermilion)            -->
    <!-- ============================================================= -->
    <!-- Ä Crossbar Bridge precisely flush inside counter -->
    <polygon points="194,120 246,120 256,150 184,150" fill="url(#b3_vermilion)" filter="url(#b3_vermilion_bloom)"/>

    <!-- R-Ä Baseline Keystone Wedge uniting foundations -->
    <polygon points="112,200 152,200 144,178 120,178" fill="url(#b3_vermilion)" filter="url(#b3_vermilion_bloom)"/>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 2: Ä ∩ V FUSION (Solar Amber)                 -->
    <!-- ============================================================= -->
    <!-- Bedrock chevron weld between Ä right foot and V left arm -->
    <polygon points="270,200 304,200 326,144 292,144" fill="url(#b3_amber)"/>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 3: V ∩ N FUSION (Hyper Cyan)                  -->
    <!-- ============================================================= -->
    <!-- Crown joint capping V right arm into N left pillar & diagonal -->
    <rect x="424" y="42" width="22" height="38" rx="2" fill="url(#b3_cyan)" filter="url(#b3_cyan_bloom)"/>
    <polygon points="468,96 496,96 522,148 494,148" fill="url(#b3_cyan)" filter="url(#b3_cyan_bloom)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#b3_vermilion)" filter="url(#b3_vermilion_bloom)"/>
    <circle cx="252" cy="18" r="14" fill="url(#b3_vermilion)" filter="url(#b3_vermilion_bloom)"/>

  </g>
</svg>'''

# =============================================================================
# 4. SHARED-STROKE SAWTOOTH MONOLITH (logo-blend-4-dovetail.svg)
# =============================================================================
def generate_blend_4():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Shared-Stroke Monolith">
  <title>Rävn — Shared-Stroke Constructivist Monolith</title>
  <defs>
    <!-- International Signal Vermilion to Solar Flame -->
    <linearGradient id="b4_vermilion" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#D70015"/>
    </linearGradient>

    <!-- Solar Amber Foundation -->
    <linearGradient id="b4_amber" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- Titanium Polar White -->
    <linearGradient id="b4_white" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#E2E8F0"/>
    </linearGradient>

    <filter id="b4_glow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.45"/>
    </filter>
  </defs>

  <g id="wordmark-shared" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- 1. CANTILEVER GIRDER FROM R WAIST INTO Ä CROSSBAR             -->
    <!-- Unbroken horizontal beam connecting R into Ä                  -->
    <!-- ============================================================= -->
    <rect x="62" y="118" width="134" height="30" rx="2" fill="url(#b4_vermilion)" filter="url(#b4_glow)"/>

    <!-- ============================================================= -->
    <!-- 2. LETTER R (Polar White)                                     -->
    <!-- ============================================================= -->
    <g fill="url(#b4_white)">
      <rect x="30" y="42" width="32" height="158" rx="2"/>
      <path d="
        M 62,42
        L 106,42
        C 138,42 138,124 106,124
        L 62,124
        Z
        M 62,74
        L 102,74
        C 112,74 112,92 102,92
        L 62,92
        Z
      " fill-rule="evenodd"/>
      <!-- Leg branches cleanly from girder underside -->
      <polygon points="86,148 144,200 108,200 62,148"/>
    </g>

    <!-- ============================================================= -->
    <!-- 3. LETTER Ä & V: THE SHARED-STROKE SAWTOOTH TECTONIC          -->
    <!-- ============================================================= -->
    
    <!-- Ä Left Diagonal (Titanium White) -->
    <polygon points="
      174,200
      140,200
      196,42
      228,42
    " fill="url(#b4_white)"/>

    <!-- Ä Crossbar (Signal Vermilion - flush with counter) -->
    <polygon points="169,118 223,118 234,148 158,148" fill="url(#b4_vermilion)" filter="url(#b4_glow)"/>

    <!-- THE MASTERPIECE: SHARED DIAGONAL STROKE                       -->
    <!-- This single descending diagonal is BOTH Ä's Right Leg AND      -->
    <!-- V's Left Arm! Highlighted in luminous Signal Vermilion.        -->
    <polygon points="
      196,42
      228,42
      284,200
      252,200
    " fill="url(#b4_vermilion)" filter="url(#b4_glow)"/>

    <!-- V Right Arm (Titanium White) rising from shared vertex -->
    <polygon points="
      252,200
      284,200
      340,42
      308,42
    " fill="url(#b4_white)"/>

    <!-- Foundation Keel at the V vertex (Solar Amber) -->
    <polygon points="244,176 292,176 284,200 252,200" fill="url(#b4_amber)"/>

    <!-- ============================================================= -->
    <!-- 4. LETTER N (Titanium White & Amber Accents)                  -->
    <!-- ============================================================= -->
    <!-- Crown link at top shoulder into N -->
    <rect x="330" y="42" width="46" height="32" rx="2" fill="url(#b4_amber)"/>

    <!-- N Body -->
    <g fill="url(#b4_white)">
      <!-- Left Pillar -->
      <rect x="366" y="42" width="32" height="158" rx="2"/>
      <!-- Descending Diagonal -->
      <polygon points="366,42 400,42 486,170 486,42 518,42 518,200 484,200 398,72 398,200 366,200"/>
    </g>

    <!-- Amber Accent on N Diagonal -->
    <polygon points="414,96 442,96 470,138 442,138" fill="url(#b4_amber)"/>

    <!-- ============================================================= -->
    <!-- 5. UMLAUT: Twin Signal Vermilion Discs                        -->
    <!-- Perfectly centered over Ä's apex at X = 212                   -->
    <!-- ============================================================= -->
    <circle cx="196" cy="18" r="14" fill="url(#b4_vermilion)" filter="url(#b4_glow)"/>
    <circle cx="228" cy="18" r="14" fill="url(#b4_vermilion)" filter="url(#b4_glow)"/>

  </g>
</svg>'''

if __name__ == "__main__":
    files = {
        "logo-blend-1-interlock.svg": generate_blend_1(),
        "logo-blend-2-ribbon.svg": generate_blend_2(),
        "logo-blend-3-chromatic.svg": generate_blend_3(),
        "logo-blend-4-dovetail.svg": generate_blend_4(),
    }
    for name, content in files.items():
        path = os.path.join(SVG_DIR, name)
        with open(path, "w") as f:
            f.write(content)
        print(f"Generated {path}")
