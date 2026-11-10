"""
Master generator for the 4 creative blending logotypes for Rävn:
1. logo-blend-1-interlock.svg (Architectural Ligature Bridge)
2. logo-blend-2-ribbon.svg    (Continuous Kinetic Gradient Track)
3. logo-blend-3-chromatic.svg (Chromatic Facet Fusion)
4. logo-blend-4-dovetail.svg  (Two-Tone Horizon Monolith)
"""
import math
import os

os.makedirs("design-logos", exist_ok=True)

# -------------------------------------------------------------------------
# 1. ARCHITECTURAL LIGATURE BRIDGE
# Clean white letters + Signal Vermilion cantilever bridge from R into Ä,
# bedrock fusion between Ä & V, top shoulder link between V & N.
# -------------------------------------------------------------------------
def make_blend_1():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 570 230" width="570" height="230" role="img" aria-label="Rävn — Architectural Ligature">
  <title>Rävn — Architectural Ligature</title>
  <defs>
    <!-- International Signal Vermilion to Warm Flame -->
    <linearGradient id="bridgeFlame" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="60%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FF9500"/>
    </linearGradient>

    <!-- Polar White -->
    <linearGradient id="bodyWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="vermilionGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.35"/>
    </filter>
  </defs>

  <g id="wordmark-ligature" transform="translate(10, 8)">

    <!-- ============================================================= -->
    <!-- THE LIGATURE BRIDGE: Solid Vermilion beam connecting R to Ä   -->
    <!-- Runs at Y: 126 to 158 (Height = 32px), perfectly aligned with -->
    <!-- R's waist and forming Ä's crossbar directly!                  -->
    <!-- ============================================================= -->
    <path d="
      M 62,126
      L 242,126
      L 242,158
      L 62,158
      Z
    " fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER R                                                      -->
    <!-- ============================================================= -->
    <!-- Vertical Stem -->
    <rect x="30" y="48" width="32" height="160" rx="2" fill="url(#bodyWhite)"/>

    <!-- Bowl (Continuous compound path) -->
    <path d="
      M 62,48
      L 100,48
      C 134,48 134,136 100,136
      L 62,136
      Z
      M 62,80
      L 96,80
      C 108,80 108,104 96,104
      L 62,104
      Z
    " fill="url(#bodyWhite)" fill-rule="evenodd"/>

    <!-- Leg: Branches out from waist to baseline -->
    <polygon points="94,136 142,208 104,208 66,136" fill="url(#bodyWhite)"/>

    <!-- ============================================================= -->
    <!-- LETTER Ä                                                      -->
    <!-- Left & Right Diagonals                                        -->
    <!-- ============================================================= -->
    <polygon points="
      216,48
      244,48
      300,208
      266,208
      230,105
      194,208
      160,208
    " fill="url(#bodyWhite)"/>

    <!-- ============================================================= -->
    <!-- Ä-to-V BASELINE BEDROCK JOINT                                 -->
    <!-- Right foot of Ä fuses with left foot of V at the baseline     -->
    <!-- ============================================================= -->
    <polygon points="266,208 300,208 316,154 282,154" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER V                                                      -->
    <!-- ============================================================= -->
    <polygon points="
      282,48
      314,48
      354,160
      394,48
      426,48
      370,208
      338,208
    " fill="url(#bodyWhite)"/>

    <!-- ============================================================= -->
    <!-- V-to-N TOP SHOULDER TIE                                       -->
    <!-- Vermilion joint capping where V connects to N                 -->
    <!-- ============================================================= -->
    <rect x="416" y="48" width="22" height="32" rx="2" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER N                                                      -->
    <!-- ============================================================= -->
    <polygon points="
      426,48
      464,48
      514,154
      514,48
      546,48
      546,208
      508,208
      458,104
      458,208
      426,208
    " fill="url(#bodyWhite)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="214" cy="22" r="16" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>
    <circle cx="254" cy="22" r="16" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

  </g>
</svg>'''
    with open("design-logos/logo-blend-1-interlock.svg", "w") as f:
        f.write(svg)
    print("Saved logo-blend-1-interlock.svg")

# -------------------------------------------------------------------------
# 2. CONTINUOUS KINETIC GRADIENT TRACK
# Single unbroken geometric band weaving R -> Ä -> V -> N
# With vibrant plumage gradient: Polar White -> Violet -> Vermilion -> Amber
# -------------------------------------------------------------------------
def make_blend_2():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 570 230" width="570" height="230" role="img" aria-label="Rävn — Kinetic Ribbon">
  <title>Rävn — Kinetic Folded Ribbon</title>
  <defs>
    <!-- Fluid Multi-Stop Kinetic Spectrum -->
    <linearGradient id="spectrumRibbon" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="22%" stop-color="#C7D2FE"/>
      <stop offset="42%" stop-color="#8B5CF6"/>
      <stop offset="65%" stop-color="#FF3B30"/>
      <stop offset="85%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <linearGradient id="amberUmlaut" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FBBF24"/>
    </linearGradient>

    <filter id="ribbonAura" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="5" stdDeviation="8" flood-color="#FF3B30" flood-opacity="0.35"/>
    </filter>
  </defs>

  <g id="wordmark-ribbon" filter="url(#ribbonAura)" fill="url(#spectrumRibbon)" transform="translate(10, 8)">

    <!-- LETTER R: Stem and Bowl -->
    <path d="
      M 30,208
      L 30,48
      L 98,48
      C 138,48 138,136 98,136
      L 62,136
      L 62,208
      Z
      M 62,80
      L 94,80
      C 106,80 106,104 94,104
      L 62,104
      Z
    " fill-rule="evenodd"/>

    <!-- R Leg sweeps down and bridges across baseline directly into Ä! -->
    <polygon points="96,136 144,208 178,208 106,136"/>

    <!-- LETTER Ä: Left stem picks up from R's baseline sweep -->
    <polygon points="
      216,48
      244,48
      300,208
      268,208
      252,164
      188,164
      174,208
      142,208
      202,48
    "/>
    <!-- Ä Triangular Counter Window -->
    <polygon points="230,104 202,136 248,136" fill="#090B10"/>

    <!-- Ä right leg loops across baseline directly into V! -->
    <polygon points="
      268,208
      300,208
      336,48
      368,48
      386,150
      422,48
      454,48
      396,208
      364,208
    "/>

    <!-- V right arm vaults directly into N! -->
    <polygon points="
      454,48
      486,48
      522,152
      522,48
      554,48
      554,208
      522,208
      486,104
      486,208
      454,208
    "/>

    <!-- UMLAUT: Glowing Solar Discs -->
    <circle cx="214" cy="22" r="16" fill="url(#amberUmlaut)"/>
    <circle cx="254" cy="22" r="16" fill="url(#amberUmlaut)"/>

  </g>
</svg>'''
    with open("design-logos/logo-blend-2-ribbon.svg", "w") as f:
        f.write(svg)
    print("Saved logo-blend-2-ribbon.svg")

# -------------------------------------------------------------------------
# 3. CHROMATIC FACET FUSION
# Crisp titanium white letters with vivid jewel-like color facets where they intersect:
# R ∩ Ä: Electric Vermilion
# Ä ∩ V: Solar Amber
# V ∩ N: Hyper Cyan
# -------------------------------------------------------------------------
def make_blend_3():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 570 230" width="570" height="230" role="img" aria-label="Rävn — Chromatic Facet Fusion">
  <title>Rävn — Chromatic Facet Fusion</title>
  <defs>
    <!-- Facet Gradients -->
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

    <linearGradient id="crispWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="facetBloom" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.35"/>
    </filter>
  </defs>

  <g id="wordmark-chromatic" transform="translate(10, 8)">

    <!-- ============================================================= -->
    <!-- LETTER R (Titanium White)                                     -->
    <!-- ============================================================= -->
    <g fill="url(#crispWhite)">
      <rect x="30" y="48" width="32" height="160" rx="2"/>
      <path d="
        M 62,48
        L 100,48
        C 134,48 134,136 100,136
        L 62,136
        Z
        M 62,80
        L 96,80
        C 108,80 108,104 96,104
        L 62,104
        Z
      " fill-rule="evenodd"/>
      <polygon points="94,136 142,208 104,208 66,136"/>
    </g>

    <!-- FUSION FACET 1: Where R leg touches Ä left stem (Signal Vermilion) -->
    <polygon points="116,170 142,208 126,208 108,180" fill="url(#neonVermilion)" filter="url(#facetBloom)"/>

    <!-- ============================================================= -->
    <!-- LETTER Ä                                                      -->
    <!-- White diagonals + Glowing Vermilion Crossbar & Umlaut         -->
    <!-- ============================================================= -->
    <g fill="url(#crispWhite)">
      <polygon points="
        216,48
        244,48
        300,208
        266,208
        252,168
        188,168
        174,208
        140,208
      "/>
    </g>

    <!-- Ä Crossbar Bridge (Vivid Signal Vermilion) -->
    <rect x="176" y="136" width="90" height="32" rx="2" fill="url(#neonVermilion)" filter="url(#facetBloom)"/>

    <!-- FUSION FACET 2: Where Ä right foot merges into V (Solar Amber) -->
    <polygon points="266,208 300,208 318,154 284,154" fill="url(#neonAmber)"/>

    <!-- ============================================================= -->
    <!-- LETTER V (Titanium White)                                     -->
    <!-- ============================================================= -->
    <g fill="url(#crispWhite)">
      <polygon points="
        282,48
        314,48
        354,160
        394,48
        426,48
        370,208
        338,208
      "/>
    </g>

    <!-- FUSION FACET 3: Where V right shoulder connects into N (Hyper-Cyan) -->
    <rect x="416" y="48" width="22" height="36" rx="2" fill="url(#neonCyan)"/>

    <!-- ============================================================= -->
    <!-- LETTER N (Titanium White with Cyan Accent on Diagonal)        -->
    <!-- ============================================================= -->
    <g fill="url(#crispWhite)">
      <polygon points="
        426,48
        464,48
        514,154
        514,48
        546,48
        546,208
        508,208
        458,104
        458,208
        426,208
      "/>
    </g>

    <!-- Cyan Facet inside N -->
    <polygon points="458,104 482,104 508,154 484,154" fill="url(#neonCyan)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="214" cy="22" r="16" fill="url(#neonVermilion)" filter="url(#facetBloom)"/>
    <circle cx="254" cy="22" r="16" fill="url(#neonVermilion)" filter="url(#facetBloom)"/>

  </g>
</svg>'''
    with open("design-logos/logo-blend-3-chromatic.svg", "w") as f:
        f.write(svg)
    print("Saved logo-blend-3-chromatic.svg")

# -------------------------------------------------------------------------
# 4. CONSTRUCTIVIST DOVETAIL HORIZON
# Continuous horizontal laser horizon uniting all 4 letters.
# Top: Pure Titanium White. Bottom: Signal Vermilion & Amber bedrock.
# -------------------------------------------------------------------------
def make_blend_4():
    svg = '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 570 230" width="570" height="230" role="img" aria-label="Rävn — Constructivist Dovetail">
  <title>Rävn — Constructivist Dovetail Horizon</title>
  <defs>
    <!-- International Signal Vermilion -->
    <linearGradient id="dovetailRed" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#D70015"/>
    </linearGradient>

    <!-- Solar Amber Foundation -->
    <linearGradient id="dovetailAmber" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FF5E3A"/>
    </linearGradient>

    <!-- Polar White Upper Body -->
    <linearGradient id="dovetailWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#E2E8F0"/>
    </linearGradient>

    <!-- Laser Pulse Beam -->
    <linearGradient id="laserPulse" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="50%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>
  </defs>

  <g id="wordmark-dovetail" transform="translate(10, 8)">

    <!-- ============================================================= -->
    <!-- UPPER HALF (Pure Titanium White Monolith)                     -->
    <!-- All letters above horizon line Y: 134                         -->
    <!-- ============================================================= -->
    <g fill="url(#dovetailWhite)">
      <!-- R Upper -->
      <path d="
        M 30,134
        L 30,48
        L 96,48
        C 134,48 134,134 96,134
        L 62,134
        Z
        M 62,80
        L 94,80
        C 106,80 106,104 94,104
        L 62,104
        Z
      " fill-rule="evenodd"/>

      <!-- Ä Upper Apex -->
      <polygon points="216,48 244,48 274,134 186,134"/>

      <!-- V Upper Shoulders -->
      <polygon points="282,48 314,48 336,134 304,134"/>
      <polygon points="394,48 426,48 394,134 362,134"/>

      <!-- N Upper Pillars -->
      <polygon points="426,134 426,48 458,48 502,134"/>
      <polygon points="514,134 514,48 546,48 546,134"/>
    </g>

    <!-- ============================================================= -->
    <!-- THE LASER HORIZON LINE                                        -->
    <!-- A vibrant 4px continuous pulse cutting across the waistline   -->
    <!-- ============================================================= -->
    <rect x="30" y="132" width="516" height="4" rx="2" fill="url(#laserPulse)"/>

    <!-- ============================================================= -->
    <!-- LOWER HALF (Interlocking Foundations in Signal Vermilion)     -->
    <!-- All letters below horizon Y: 136                              -->
    <!-- ============================================================= -->
    <g fill="url(#dovetailRed)">
      <!-- R Lower: Stem + Leg -->
      <rect x="30" y="136" width="32" height="72" rx="2"/>
      <polygon points="74,136 94,136 142,208 106,208"/>

      <!-- Ä Lower: Legs + Interlocking Base -->
      <polygon points="186,136 214,136 194,168 170,208 138,208"/>
      <polygon points="274,136 300,208 268,208 246,168 246,136"/>

      <!-- V Lower: Vertex Keel -->
      <polygon points="304,136 394,136 370,208 338,208"/>

      <!-- N Lower: Pillars -->
      <rect x="426" y="136" width="32" height="72" rx="2"/>
      <rect x="514" y="136" width="32" height="72" rx="2"/>
    </g>

    <!-- Golden Keystone in Ä Center & N Diagonal -->
    <polygon points="204,168 252,168 228,136" fill="url(#dovetailAmber)"/>
    <polygon points="458,136 500,136 514,168 514,208 490,208" fill="url(#dovetailAmber)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="214" cy="22" r="16" fill="url(#dovetailRed)"/>
    <circle cx="254" cy="22" r="16" fill="url(#dovetailRed)"/>

  </g>
</svg>'''
    with open("design-logos/logo-blend-4-dovetail.svg", "w") as f:
        f.write(svg)
    print("Saved logo-blend-4-dovetail.svg")

if __name__ == "__main__":
    make_blend_1()
    make_blend_2()
    make_blend_3()
    make_blend_4()
