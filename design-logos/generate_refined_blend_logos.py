"""
Refined generator for the 4 creative blending logotypes for Rävn.
Zero background, name ONLY, uniform stroke width, vibrant brand colors, creative blending.
"""
import os

SVG_DIR = "design-logos"
os.makedirs(SVG_DIR, exist_ok=True)

# -----------------------------------------------------------------------------
# 1. ARCHITECTURAL LIGATURE BRIDGE (logo-blend-1-interlock.svg)
# -----------------------------------------------------------------------------
def make_blend_1():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Architectural Ligature Bridge">
  <title>Rävn — Architectural Ligature Bridge</title>
  <defs>
    <linearGradient id="bridgeVermilion" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="70%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FF9500"/>
    </linearGradient>

    <linearGradient id="bodyPolarWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="vermilionGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.45"/>
    </filter>
  </defs>

  <g id="wordmark-ligature" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- 1. CANTILEVER BRIDGE: R WAIST INTO Ä CROSSBAR                 -->
    <!-- A solid 32px Signal Vermilion beam uniting R and Ä            -->
    <!-- ============================================================= -->
    <rect x="62" y="118" width="186" height="30" rx="2" fill="url(#bridgeVermilion)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- 2. Ä-TO-V BASELINE BEDROCK JOINT                              -->
    <!-- Foundation weld uniting Ä right foot with V left diagonal     -->
    <!-- ============================================================= -->
    <polygon points="270,200 304,200 326,140 292,140" fill="url(#bridgeVermilion)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- 3. V-TO-N TOP SHOULDER CROWN                                  -->
    <!-- Joint capping V right ascending arm into N left pillar        -->
    <!-- ============================================================= -->
    <rect x="424" y="42" width="22" height="36" rx="2" fill="url(#bridgeVermilion)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- 4. WHITE ARCHITECTURAL BODIES                                 -->
    <!-- ============================================================= -->
    <g fill="url(#bodyPolarWhite)">
      <!-- LETTER R -->
      <!-- Vertical Stem (32px width) -->
      <rect x="30" y="42" width="32" height="158" rx="2"/>

      <!-- Bowl -->
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

      <!-- Leg -->
      <polygon points="90,122 144,200 108,200 62,132"/>

      <!-- LETTER Ä -->
      <!-- Left & Right Diagonals (34px horizontal width = 32px perpendicular) -->
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
      <!-- Left & Right Diagonals -->
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
      <!-- Left Pillar, Diagonal, Right Pillar -->
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
    <!-- 5. UMLAUT: Twin Signal Vermilion Discs                        -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#bridgeVermilion)" filter="url(#vermilionGlow)"/>
    <circle cx="252" cy="18" r="14" fill="url(#bridgeVermilion)" filter="url(#vermilionGlow)"/>

  </g>
</svg>'''
    with open(f"{SVG_DIR}/logo-blend-1-interlock.svg", "w") as f:
        f.write(svg)
    print("Wrote logo-blend-1-interlock.svg")

# -----------------------------------------------------------------------------
# 2. CONTINUOUS KINETIC PULSE THREAD (logo-blend-2-ribbon.svg)
# Lance Wyman / Neo-Brutalist unbroken laser current threading all 4 letters
# -----------------------------------------------------------------------------
def make_blend_2():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Kinetic Pulse Thread">
  <title>Rävn — Kinetic Pulse Thread</title>
  <defs>
    <!-- Radiant Multi-Stop Laser Pulse Thread -->
    <linearGradient id="laserPulseFlow" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF3B30"/>
      <stop offset="35%" stop-color="#FF453A"/>
      <stop offset="65%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- Polar White Body -->
    <linearGradient id="monumentWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="laserAura" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="3" stdDeviation="5" flood-color="#FF3B30" flood-opacity="0.5"/>
    </filter>
  </defs>

  <g id="wordmark-pulse" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- WHITE BRUTALIST STRUCTURAL BODIES                             -->
    <!-- Precision calibrated 32px stroke width                        -->
    <!-- ============================================================= -->
    <g fill="url(#monumentWhite)">
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
    <!-- THE UNBROKEN KINETIC PULSE THREAD                             -->
    <!-- An electric 20px laser current running through the centerline -->
    <!-- of all 4 letters, locking them together into a single system  -->
    <!-- ============================================================= -->
    <g fill="url(#laserPulseFlow)" filter="url(#laserAura)">
      <!-- Thread segment through R waist into Ä crossbar -->
      <rect x="30" y="124" width="220" height="18" rx="2"/>

      <!-- Thread segment across Ä to V -->
      <rect x="250" y="124" width="80" height="18" rx="2"/>

      <!-- Thread segment across V to N -->
      <rect x="330" y="124" width="104" height="18" rx="2"/>

      <!-- Thread segment through N -->
      <rect x="434" y="124" width="120" height="18" rx="2"/>
    </g>

    <!-- Baseline Footing Welds uniting the letter foundations -->
    <polygon points="108,200 144,200 166,168 130,168" fill="url(#laserPulseFlow)" filter="url(#laserAura)"/>
    <polygon points="270,200 304,200 324,152 290,152" fill="url(#laserPulseFlow)" filter="url(#laserAura)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Glowing Solar Discs                                   -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#laserPulseFlow)" filter="url(#laserAura)"/>
    <circle cx="252" cy="18" r="14" fill="url(#laserPulseFlow)" filter="url(#laserAura)"/>

  </g>
</svg>'''
    with open(f"{SVG_DIR}/logo-blend-2-ribbon.svg", "w") as f:
        f.write(svg)
    print("Wrote logo-blend-2-ribbon.svg")

# -----------------------------------------------------------------------------
# 3. CHROMATIC PRISM FUSION (logo-blend-3-chromatic.svg)
# -----------------------------------------------------------------------------
def make_blend_3():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Chromatic Prism Fusion">
  <title>Rävn — Chromatic Prism Fusion</title>
  <defs>
    <!-- Prismatic Facet Gradients -->
    <linearGradient id="neonVermilion" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#FF2D55"/>
    </linearGradient>

    <linearGradient id="neonAmber" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <linearGradient id="neonCyan" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#00F0FF"/>
      <stop offset="100%" stop-color="#007AFF"/>
    </linearGradient>

    <linearGradient id="titaniumWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="vermilionBloom" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.45"/>
    </filter>
    <filter id="cyanBloom" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#00F0FF" flood-opacity="0.45"/>
    </filter>
  </defs>

  <g id="wordmark-chromatic" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- LETTER R (Titanium Polar White)                               -->
    <!-- ============================================================= -->
    <g fill="url(#titaniumWhite)">
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
    </g>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 1: R-Ä INTERLOCK (International Signal Vermilion) -->
    <!-- Vermilion crossbar bridge + baseline foot keystone            -->
    <!-- ============================================================= -->
    <rect x="180" y="120" width="82" height="30" rx="2" fill="url(#neonVermilion)" filter="url(#vermilionBloom)"/>
    <polygon points="108,200 144,200 166,164 130,164" fill="url(#neonVermilion)" filter="url(#vermilionBloom)"/>

    <!-- ============================================================= -->
    <!-- LETTER Ä (Titanium Polar White)                               -->
    <!-- ============================================================= -->
    <polygon points="
      220,42
      250,42
      304,200
      270,200
      235,96
      200,200
      166,200
    " fill="url(#titaniumWhite)"/>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 2: Ä-V BEDROCK WELD (Solar Amber)             -->
    <!-- ============================================================= -->
    <polygon points="270,200 304,200 326,140 292,140" fill="url(#neonAmber)"/>

    <!-- ============================================================= -->
    <!-- LETTER V (Titanium Polar White)                               -->
    <!-- ============================================================= -->
    <polygon points="
      290,42
      324,42
      362,154
      400,42
      434,42
      379,200
      345,200
    " fill="url(#titaniumWhite)"/>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 3: V-N VAULT JOINT (Hyper Cyan)               -->
    <!-- ============================================================= -->
    <rect x="424" y="42" width="22" height="36" rx="2" fill="url(#neonCyan)" filter="url(#cyanBloom)"/>
    <polygon points="468,96 494,96 522,148 496,148" fill="url(#neonCyan)" filter="url(#cyanBloom)"/>

    <!-- ============================================================= -->
    <!-- LETTER N (Titanium Polar White)                               -->
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
    " fill="url(#titaniumWhite)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#neonVermilion)" filter="url(#vermilionBloom)"/>
    <circle cx="252" cy="18" r="14" fill="url(#neonVermilion)" filter="url(#vermilionBloom)"/>

  </g>
</svg>'''
    with open(f"{SVG_DIR}/logo-blend-3-chromatic.svg", "w") as f:
        f.write(svg)
    print("Wrote logo-blend-3-chromatic.svg")

# -----------------------------------------------------------------------------
# 4. CONSTRUCTIVIST DOVETAIL HORIZON (logo-blend-4-dovetail.svg)
# -----------------------------------------------------------------------------
def make_blend_4():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Constructivist Dovetail Horizon">
  <title>Rävn — Constructivist Dovetail Horizon</title>
  <defs>
    <!-- Signal Vermilion Bedrock -->
    <linearGradient id="dovetailVermilion" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#D70015"/>
    </linearGradient>

    <!-- Solar Amber Keystone -->
    <linearGradient id="dovetailAmber" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FF5E3A"/>
    </linearGradient>

    <!-- Titanium Polar White Upper Monolith -->
    <linearGradient id="dovetailWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#E2E8F0"/>
    </linearGradient>

    <!-- Laser Horizon Line -->
    <linearGradient id="horizonLaser" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="50%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <filter id="laserGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="2" stdDeviation="4" flood-color="#FF3B30" flood-opacity="0.5"/>
    </filter>
  </defs>

  <g id="wordmark-dovetail" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- UPPER HALF (Titanium Polar White Monolith)                    -->
    <!-- All letters above horizon Y: 124                              -->
    <!-- ============================================================= -->
    <g fill="url(#dovetailWhite)">
      <!-- R Upper -->
      <path d="
        M 30,124
        L 30,42
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

      <!-- Ä Upper Apex -->
      <polygon points="220,42 250,42 278,124 192,124"/>

      <!-- V Upper Shoulders -->
      <polygon points="290,42 324,42 344,124 310,124"/>
      <polygon points="400,42 434,42 400,124 366,124"/>

      <!-- N Upper Pillars -->
      <polygon points="434,124 434,42 468,42 506,124"/>
      <polygon points="520,124 520,42 554,42 554,124"/>
    </g>

    <!-- ============================================================= -->
    <!-- THE LASER HORIZON LINE                                        -->
    <!-- Continuous 4px neon horizon running through optical waist     -->
    <!-- ============================================================= -->
    <rect x="30" y="122" width="524" height="4" rx="2" fill="url(#horizonLaser)" filter="url(#laserGlow)"/>

    <!-- ============================================================= -->
    <!-- LOWER HALF (Interlocking Signal Vermilion Bedrock)            -->
    <!-- All letters below horizon Y: 126                              -->
    <!-- ============================================================= -->
    <g fill="url(#dovetailVermilion)">
      <!-- R Lower Stem & Leg -->
      <rect x="30" y="126" width="32" height="74" rx="2"/>
      <polygon points="76,126 100,126 144,200 108,200"/>

      <!-- Ä Lower Legs -->
      <polygon points="192,126 216,126 196,160 166,200 134,200"/>
      <polygon points="278,126 304,200 270,200 248,160 248,126"/>

      <!-- V Lower Keel -->
      <polygon points="310,126 400,126 379,200 345,200"/>

      <!-- N Lower Pillars -->
      <rect x="434" y="126" width="34" height="74" rx="2"/>
      <rect x="520" y="126" width="34" height="74" rx="2"/>
    </g>

    <!-- ============================================================= -->
    <!-- SOLAR AMBER KEYSTONES                                         -->
    <!-- ============================================================= -->
    <!-- Keystone in Ä Center Counter -->
    <polygon points="204,160 258,160 231,126" fill="url(#dovetailAmber)"/>

    <!-- Keystone along N Diagonal -->
    <polygon points="468,126 506,126 520,154 520,200 496,200" fill="url(#dovetailAmber)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#dovetailVermilion)"/>
    <circle cx="252" cy="18" r="14" fill="url(#dovetailVermilion)"/>

  </g>
</svg>'''
    with open(f"{SVG_DIR}/logo-blend-4-dovetail.svg", "w") as f:
        f.write(svg)
    print("Wrote logo-blend-4-dovetail.svg")

if __name__ == "__main__":
    make_blend_1()
    make_blend_2()
    make_blend_3()
    make_blend_4()
