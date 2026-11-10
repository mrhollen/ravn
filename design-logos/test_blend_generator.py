"""
Precision generator and Playwright renderer for 4 creative blending logotypes for Rävn.
"""
import os
import subprocess

SVG_DIR = "design-logos"
os.makedirs(SVG_DIR, exist_ok=True)

# =============================================================================
# 1. ARCHITECTURAL LIGATURE BRIDGE (logo-blend-1-interlock.svg)
# =============================================================================
def generate_blend_1():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Architectural Ligature">
  <title>Rävn — Architectural Ligature Bridge</title>
  <defs>
    <!-- International Signal Vermilion to Warm Amber Flame -->
    <linearGradient id="bridgeFlame" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="65%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FF9500"/>
    </linearGradient>

    <!-- Polar White Letterform -->
    <linearGradient id="polarWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EAEAEA"/>
    </linearGradient>

    <filter id="vermilionGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.4"/>
    </filter>
  </defs>

  <g id="wordmark-ligature" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- CANTILEVER LIGATURE BEAM (R into Ä)                           -->
    <!-- Continuous horizontal Vermilion girder from R waist to Ä crossbar -->
    <!-- ============================================================= -->
    <rect x="62" y="118" width="180" height="30" rx="2" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER R                                                      -->
    <!-- ============================================================= -->
    <!-- Vertical Stem -->
    <rect x="30" y="42" width="32" height="158" rx="2" fill="url(#polarWhite)"/>

    <!-- Bowl (Continuous smooth compound path) -->
    <path d="
      M 62,42
      L 106,42
      C 138,42 138,124 106,124
      L 62,124
      Z
      M 62,74
      L 102,74
      C 112,74 112,94 102,94
      L 62,94
      Z
    " fill="url(#polarWhite)" fill-rule="evenodd"/>

    <!-- Leg (Descending structural diagonal) -->
    <polygon points="90,122 144,200 108,200 62,132" fill="url(#polarWhite)"/>

    <!-- ============================================================= -->
    <!-- LETTER Ä                                                      -->
    <!-- Left & Right Diagonals                                        -->
    <!-- ============================================================= -->
    <polygon points="
      220,42
      250,42
      304,200
      270,200
      235,96
      200,200
      166,200
    " fill="url(#polarWhite)"/>

    <!-- ============================================================= -->
    <!-- Ä-TO-V BASELINE BEDROCK JOINT                                 -->
    <!-- Vermilion foundation weld uniting Ä right foot and V left arm -->
    <!-- ============================================================= -->
    <polygon points="270,200 304,200 324,142 290,142" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER V                                                      -->
    <!-- ============================================================= -->
    <polygon points="
      290,42
      324,42
      362,154
      400,42
      434,42
      379,200
      345,200
    " fill="url(#polarWhite)"/>

    <!-- ============================================================= -->
    <!-- V-TO-N TOP SHOULDER CROWN                                     -->
    <!-- Signal Vermilion joint capping V right arm into N left pillar -->
    <!-- ============================================================= -->
    <rect x="424" y="42" width="22" height="34" rx="2" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER N                                                      -->
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
    " fill="url(#polarWhite)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>
    <circle cx="252" cy="18" r="14" fill="url(#bridgeFlame)" filter="url(#vermilionGlow)"/>

  </g>
</svg>'''

# =============================================================================
# 2. CONTINUOUS KINETIC RIBBON (logo-blend-2-ribbon.svg)
# Clean folded origami planes with no self-intersecting path bugs
# =============================================================================
def generate_blend_2():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Kinetic Folded Ribbon">
  <title>Rävn — Kinetic Folded Ribbon</title>
  <defs>
    <!-- Fluid Spectrum Gradient along the ribbon path -->
    <linearGradient id="spectrumGradient" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="20%" stop-color="#C7D2FE"/>
      <stop offset="38%" stop-color="#8B5CF6"/>
      <stop offset="62%" stop-color="#FF3B30"/>
      <stop offset="85%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- Shading for 3D folded facet depths -->
    <linearGradient id="foldShadow" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#000000" stop-opacity="0.3"/>
      <stop offset="100%" stop-color="#000000" stop-opacity="0.0"/>
    </linearGradient>

    <linearGradient id="solarAmberUmlaut" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF3B30"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <filter id="ribbonAura" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="5" stdDeviation="7" flood-color="#FF3B30" flood-opacity="0.3"/>
    </filter>
  </defs>

  <g id="wordmark-ribbon" filter="url(#ribbonAura)" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- R STEM & BOWL                                                 -->
    <!-- ============================================================= -->
    <!-- Vertical Stem -->
    <rect x="30" y="42" width="32" height="158" rx="2" fill="url(#spectrumGradient)"/>

    <!-- Bowl -->
    <path d="
      M 62,42
      L 106,42
      C 138,42 138,124 106,124
      L 62,124
      Z
      M 62,74
      L 102,74
      C 112,74 112,94 102,94
      L 62,94
      Z
    " fill="url(#spectrumGradient)" fill-rule="evenodd"/>

    <!-- ============================================================= -->
    <!-- R LEG SWEEPING DIRECTLY INTO Ä LEFT LEG                       -->
    <!-- The diagonal leg of R sweeps across the baseline and folds    -->
    <!-- straight up into the left diagonal of Ä!                      -->
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
    " fill="url(#spectrumGradient)"/>

    <!-- Fold shadow where R leg passes under Ä -->
    <polygon points="118,122 138,200 120,200 90,122" fill="url(#foldShadow)"/>

    <!-- ============================================================= -->
    <!-- Ä CROSSBAR & RIGHT LEG                                        -->
    <!-- ============================================================= -->
    <!-- Crossbar linking Ä diagonals -->
    <rect x="184" y="122" width="80" height="30" rx="2" fill="url(#spectrumGradient)"/>

    <!-- Right diagonal of Ä descends and vaults into V left arm!       -->
    <polygon points="
      220,42
      252,42
      306,200
      340,42
      372,42
      322,200
      288,200
      272,152
      236,152
    " fill="url(#spectrumGradient)"/>

    <!-- ============================================================= -->
    <!-- V VERTEX & RIGHT ARM VAULTING INTO N                          -->
    <!-- ============================================================= -->
    <polygon points="
      322,200
      372,42
      404,42
      360,192
      378,200
      432,42
      466,42
      412,200
      378,200
    " fill="url(#spectrumGradient)"/>

    <!-- ============================================================= -->
    <!-- LETTER N                                                      -->
    <!-- Left pillar, descending diagonal, right pillar                -->
    <!-- ============================================================= -->
    <polygon points="
      434,42
      466,42
      522,148
      522,42
      554,42
      554,200
      522,200
      466,96
      466,200
      434,200
    " fill="url(#spectrumGradient)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Glowing Solar Discs                                   -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#solarAmberUmlaut)"/>
    <circle cx="252" cy="18" r="14" fill="url(#solarAmberUmlaut)"/>

  </g>
</svg>'''

# =============================================================================
# 3. CHROMATIC PRISM FUSION (logo-blend-3-chromatic.svg)
# Crisp Titanium White letter bodies with vibrant glowing intersection facets
# =============================================================================
def generate_blend_3():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Chromatic Prism Fusion">
  <title>Rävn — Chromatic Prism Fusion</title>
  <defs>
    <!-- Facet 1: Electric Signal Vermilion -->
    <linearGradient id="facetVermilion" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#FF2D55"/>
    </linearGradient>

    <!-- Facet 2: Solar Gold Amber -->
    <linearGradient id="facetAmber" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <!-- Facet 3: Hyper Cyan -->
    <linearGradient id="facetCyan" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#00F0FF"/>
      <stop offset="100%" stop-color="#007AFF"/>
    </linearGradient>

    <!-- Titanium Polar White Letter Body -->
    <linearGradient id="titaniumWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>

    <filter id="facetGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#FF3B30" flood-opacity="0.4"/>
    </filter>
    <filter id="cyanGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="4" stdDeviation="6" flood-color="#00F0FF" flood-opacity="0.4"/>
    </filter>
  </defs>

  <g id="wordmark-chromatic" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- LETTER R (Titanium White)                                     -->
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
        C 112,74 112,94 102,94
        L 62,94
        Z
      " fill-rule="evenodd"/>
      <polygon points="90,122 144,200 108,200 62,132"/>
    </g>

    <!-- ============================================================= -->
    <!-- CHROMATIC FACET 1: R-Ä OVERLAP & CROSSBAR (Signal Vermilion) -->
    <!-- Where R leg overlaps Ä left diagonal, plus Ä crossbar         -->
    <!-- ============================================================= -->
    <!-- Ä Crossbar -->
    <rect x="182" y="122" width="80" height="30" rx="2" fill="url(#facetVermilion)" filter="url(#facetGlow)"/>

    <!-- R-Ä Base Interlocking Keystone -->
    <polygon points="108,200 144,200 166,168 130,168" fill="url(#facetVermilion)" filter="url(#facetGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER Ä (Titanium White)                                     -->
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
    <!-- CHROMATIC FACET 2: Ä-V FUSION JOINT (Solar Amber)             -->
    <!-- Where Ä right diagonal fuses with V left diagonal             -->
    <!-- ============================================================= -->
    <polygon points="270,200 304,200 324,142 290,142" fill="url(#facetAmber)"/>

    <!-- ============================================================= -->
    <!-- LETTER V (Titanium White)                                     -->
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
    <!-- CHROMATIC FACET 3: V-N TOP SHOULDER & DIAGONAL (Hyper Cyan)   -->
    <!-- Where V right arm merges into N left vertical & diagonal      -->
    <!-- ============================================================= -->
    <rect x="424" y="42" width="22" height="36" rx="2" fill="url(#facetCyan)" filter="url(#cyanGlow)"/>
    <polygon points="468,96 492,96 522,148 498,148" fill="url(#facetCyan)" filter="url(#cyanGlow)"/>

    <!-- ============================================================= -->
    <!-- LETTER N (Titanium White)                                     -->
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
    <circle cx="218" cy="18" r="14" fill="url(#facetVermilion)" filter="url(#facetGlow)"/>
    <circle cx="252" cy="18" r="14" fill="url(#facetVermilion)" filter="url(#facetGlow)"/>

  </g>
</svg>'''

# =============================================================================
# 4. CONSTRUCTIVIST DOVETAIL HORIZON (logo-blend-4-dovetail.svg)
# Monolithic titanium white upper half, interlocking Vermilion & Amber lower
# =============================================================================
def generate_blend_4():
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 580 230" width="580" height="230" role="img" aria-label="Rävn — Constructivist Dovetail">
  <title>Rävn — Constructivist Dovetail Horizon</title>
  <defs>
    <!-- Deep International Signal Vermilion -->
    <linearGradient id="monolithVermilion" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#D70015"/>
    </linearGradient>

    <!-- Solar Amber Foundation -->
    <linearGradient id="monolithAmber" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FF5E3A"/>
    </linearGradient>

    <!-- Titanium Polar White -->
    <linearGradient id="monolithWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#E2E8F0"/>
    </linearGradient>

    <!-- Neon Horizon Laser Pulse -->
    <linearGradient id="horizonBeam" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="50%" stop-color="#FF9500"/>
      <stop offset="100%" stop-color="#FFCC00"/>
    </linearGradient>

    <filter id="beamGlow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="2" stdDeviation="4" flood-color="#FF3B30" flood-opacity="0.5"/>
    </filter>
  </defs>

  <g id="wordmark-dovetail" transform="translate(10, 5)">

    <!-- ============================================================= -->
    <!-- UPPER TIER (Titanium Polar White Monolith)                    -->
    <!-- All letter structures above horizon Y: 124                    -->
    <!-- ============================================================= -->
    <g fill="url(#monolithWhite)">
      <!-- R Upper Stem & Bowl -->
      <path d="
        M 30,124
        L 30,42
        L 106,42
        C 138,42 138,124 106,124
        L 62,124
        Z
        M 62,74
        L 102,74
        C 112,74 112,94 102,94
        L 62,94
        Z
      " fill-rule="evenodd"/>

      <!-- Ä Upper Apex -->
      <polygon points="220,42 250,42 278,124 192,124"/>

      <!-- V Upper Shoulders -->
      <polygon points="290,42 324,42 344,124 310,124"/>
      <polygon points="400,42 434,42 400,124 366,124"/>

      <!-- N Upper Pillars & Diagonal -->
      <polygon points="434,124 434,42 468,42 506,124"/>
      <polygon points="520,124 520,42 554,42 554,124"/>
    </g>

    <!-- ============================================================= -->
    <!-- THE CONTINUOUS LASER HORIZON BEAM                             -->
    <!-- A vibrant 4px continuous line uniting all 4 letters at waist  -->
    <!-- ============================================================= -->
    <rect x="30" y="122" width="524" height="4" rx="2" fill="url(#horizonBeam)" filter="url(#beamGlow)"/>

    <!-- ============================================================= -->
    <!-- LOWER TIER (Signal Vermilion Interlocking Bedrock)            -->
    <!-- All letter structures below horizon Y: 126                    -->
    <!-- ============================================================= -->
    <g fill="url(#monolithVermilion)">
      <!-- R Lower Stem & Leg -->
      <rect x="30" y="126" width="32" height="74" rx="2"/>
      <polygon points="76,126 100,126 144,200 108,200"/>

      <!-- Ä Lower Legs -->
      <polygon points="192,126 216,126 196,160 166,200 134,200"/>
      <polygon points="278,126 304,200 270,200 248,160 248,126"/>

      <!-- V Lower Vertex -->
      <polygon points="310,126 400,126 379,200 345,200"/>

      <!-- N Lower Pillars -->
      <rect x="434" y="126" width="34" height="74" rx="2"/>
      <rect x="520" y="126" width="34" height="74" rx="2"/>
    </g>

    <!-- ============================================================= -->
    <!-- SOLAR AMBER INTERLOCKING KEYSTONES                            -->
    <!-- ============================================================= -->
    <!-- Keystone in Ä Center Counter -->
    <polygon points="204,160 258,160 231,126" fill="url(#monolithAmber)"/>

    <!-- Keystone along N Descending Diagonal -->
    <polygon points="468,126 506,126 520,154 520,200 496,200" fill="url(#monolithAmber)"/>

    <!-- ============================================================= -->
    <!-- UMLAUT: Twin Signal Vermilion Discs                           -->
    <!-- ============================================================= -->
    <circle cx="218" cy="18" r="14" fill="url(#monolithVermilion)"/>
    <circle cx="252" cy="18" r="14" fill="url(#monolithVermilion)"/>

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
        print(f"Wrote {path}")
