"""
Master generator for the 4 stylized all-caps logotypes for Rävn.
Pristine typography, mathematically exact vector outlines, optical kerning, and zero visual seams.
"""
import os

os.makedirs("design-logos", exist_ok=True)

# -------------------------------------------------------------------------
# LOGO 1: NORDIC RUNIC GEOMETRIC (Monoline Minimalist)
# -------------------------------------------------------------------------
def build_logo_1():
    # Baseline: 320, Cap: 160 (Height: 160)
    # Monoline stroke weight: 22px
    # Center: 600
    
    r_path = """
      M 265,320
      L 265,160
      L 345,160
      C 378,160 394,176 394,203
      C 394,230 378,246 345,246
      L 396,320
      L 368,320
      L 322,246
      L 287,246
      L 287,320
      Z
      M 287,180
      L 340,180
      C 362,180 372,189 372,203
      C 372,217 362,226 340,226
      L 287,226
      Z
    """

    a_path = """
      M 505,160
      L 580,320
      L 554,320
      L 537,282
      L 473,282
      L 456,320
      L 430,320
      Z
      M 505,198
      L 481,264
      L 529,264
      Z
    """

    v_path = """
      M 605,160
      L 631,160
      L 680,278
      L 729,160
      L 755,160
      L 692,320
      L 668,320
      Z
    """

    n_path = """
      M 785,160
      L 807,160
      L 893,286
      L 893,160
      L 915,160
      L 915,320
      L 893,320
      L 807,194
      L 807,320
      L 785,320
      Z
    """

    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 500" width="1200" height="500" role="img" aria-label="Rävn Logotype - Nordic Runic Geometric">
  <title>Rävn - Logotype 01: Nordic Runic Geometric</title>
  <defs>
    <linearGradient id="bg1" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#0E131F"/>
      <stop offset="50%" stop-color="#090C14"/>
      <stop offset="100%" stop-color="#04060A"/>
    </linearGradient>
    <radialGradient id="glow1" cx="50%" cy="48%" r="50%">
      <stop offset="0%" stop-color="#6366F1" stop-opacity="0.16"/>
      <stop offset="60%" stop-color="#38BDF8" stop-opacity="0.04"/>
      <stop offset="100%" stop-color="#090C14" stop-opacity="0"/>
    </radialGradient>
    <linearGradient id="titaniumGrad" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="60%" stop-color="#E2E8F0"/>
      <stop offset="100%" stop-color="#CBD5E1"/>
    </linearGradient>
    <linearGradient id="runeGold" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FDE047"/>
      <stop offset="60%" stop-color="#F59E0B"/>
      <stop offset="100%" stop-color="#D97706"/>
    </linearGradient>
    <filter id="amberGlow" x="-30%" y="-30%" width="160%" height="160%">
      <feGaussianBlur stdDeviation="5" result="b"/>
      <feMerge>
        <feMergeNode in="b"/>
        <feMergeNode in="SourceGraphic"/>
      </feMerge>
    </filter>
  </defs>

  <rect width="1200" height="500" fill="url(#bg1)"/>
  <rect width="1200" height="500" fill="url(#glow1)"/>

  <!-- Architectural Hairline Grid -->
  <g opacity="0.15" stroke="#94A3B8" stroke-width="1">
    <line x1="120" y1="160" x2="1080" y2="160" stroke="#38BDF8" stroke-dasharray="2 4"/>
    <line x1="120" y1="320" x2="1080" y2="320" stroke="#38BDF8" stroke-dasharray="2 4"/>
    <line x1="120" y1="240" x2="1080" y2="240" stroke-dasharray="4 6"/>
  </g>

  <!-- Technical Specimen Metadata Header -->
  <g font-family="ui-monospace, SFMono-Regular, Menlo, Monaco, monospace" font-size="11" fill="#64748B" letter-spacing="0.25em">
    <text x="120" y="80">RÄVN · LOGOTYPE SPEC 01</text>
    <text x="600" y="80" text-anchor="middle">SCANDINAVIAN GEOMETRIC MONOLINE</text>
    <text x="1080" y="80" text-anchor="end">GRID: 60°/90° · UNIFIED PATHS</text>
  </g>

  <!-- MAIN WORDMARK: Single Compound Path per Letter -->
  <g id="wordmark-1" fill="url(#titaniumGrad)">
    <path d="{r_path}" fill-rule="evenodd"/>
    <path d="{a_path}" fill-rule="evenodd"/>
    <path d="{v_path}"/>
    <path d="{n_path}"/>
  </g>

  <!-- Runic Diamonds in Nordic Amber -->
  <polygon points="492,118 501,127 492,136 483,127" fill="url(#runeGold)" filter="url(#amberGlow)"/>
  <polygon points="518,118 527,127 518,136 509,127" fill="url(#runeGold)" filter="url(#amberGlow)"/>

  <!-- Tagline -->
  <g font-family="system-ui, -apple-system, sans-serif" text-anchor="middle">
    <text x="600" y="405" font-size="12" font-weight="600" fill="#94A3B8" letter-spacing="0.45em">MCP-FIRST KANBAN · AGENT COORDINATION</text>
    <circle cx="600" cy="424" r="2.5" fill="#F59E0B"/>
  </g>
</svg>'''
    with open("design-logos/logo-1-nordic-geometric.svg", "w") as f:
        f.write(svg)
    print("Saved logo-1-nordic-geometric.svg")

# -------------------------------------------------------------------------
# LOGO 2: CYBERNETIC STENCIL / AGENT PROTOCOL (Modular Aerospace)
# -------------------------------------------------------------------------
def build_logo_2():
    # Stencil cuts, 45° chamfers, dual phosphor LED nodes
    
    # R: Upper piece (stem + bowl with 45° chamfers), lower stem, diagonal strut
    r_path = """
      M 260,160 
      L 345,160 
      L 388,203 
      L 345,236 
      L 260,236 
      Z
      M 284,180 
      L 338,180 
      L 364,203 
      L 338,216 
      L 284,216 
      Z
      M 260,248
      L 284,248
      L 284,320
      L 270,320
      L 260,310
      Z
      M 324,248
      L 348,248
      L 392,312
      L 392,320
      L 364,320
      L 324,260
      Z
    """

    # Ä: Stencil chevron with crossbar data bridge
    a_path = """
      M 502,160
      L 535,236
      L 511,236
      L 502,214
      L 493,236
      L 469,236
      Z
      M 464,248
      L 458,262
      L 546,262
      L 540,248
      Z
      M 452,274
      L 432,320
      L 456,320
      L 468,274
      Z
      M 552,274
      L 572,320
      L 548,320
      L 536,274
      Z
    """

    # V: Two laser-machined avionics wings meeting with a central 6px data slit
    v_path = """
      M 610,160
      L 634,160
      L 676,280
      L 676,320
      L 664,320
      Z
      M 750,160
      L 726,160
      L 684,280
      L 684,320
      L 696,320
      Z
    """

    # N: Left and right stencil columns, continuous diagonal vector
    n_path = """
      M 785,160
      L 807,160
      L 807,236
      L 785,236
      Z
      M 785,248
      L 807,248
      L 807,320
      L 785,320
      Z
      M 893,160
      L 915,160
      L 915,236
      L 893,236
      Z
      M 893,248
      L 915,248
      L 915,320
      L 893,320
      Z
      M 807,165
      L 829,165
      L 893,315
      L 871,315
      Z
    """

    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 500" width="1200" height="500" role="img" aria-label="Rävn Logotype - Cybernetic Stencil Matrix">
  <title>Rävn - Logotype 02: Cybernetic Stencil Matrix</title>
  <defs>
    <linearGradient id="bg2" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#0B0E14"/>
      <stop offset="50%" stop-color="#07090D"/>
      <stop offset="100%" stop-color="#030407"/>
    </linearGradient>
    <pattern id="dotGrid" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
      <circle cx="12" cy="12" r="1" fill="#1E293B" opacity="0.6"/>
    </pattern>
    <linearGradient id="cyberCore" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="50%" stop-color="#E0F2FE"/>
      <stop offset="100%" stop-color="#BAE6FD"/>
    </linearGradient>
    <filter id="cyanGlow" x="-50%" y="-50%" width="200%" height="200%">
      <feGaussianBlur stdDeviation="8" result="b1"/>
      <feGaussianBlur stdDeviation="3" result="b2"/>
      <feMerge>
        <feMergeNode in="b1"/>
        <feMergeNode in="b2"/>
        <feMergeNode in="SourceGraphic"/>
      </feMerge>
    </filter>
  </defs>

  <rect width="1200" height="500" fill="url(#bg2)"/>
  <rect width="1200" height="500" fill="url(#dotGrid)"/>

  <!-- Server Chassis Accents & Telemetry HUD -->
  <g font-family="ui-monospace, SFMono-Regular, Menlo, Monaco, monospace" font-size="10" fill="#475569" letter-spacing="0.15em">
    <text x="80" y="55">SYS::RAVN-DAEMON // NODE_01</text>
    <text x="600" y="55" text-anchor="middle">TRANSPORT::STREAMABLE-HTTP [PORT: 9311]</text>
    <text x="1120" y="55" text-anchor="end">MEM::PGO-SQLITE · CAS-LOCK: ON</text>
    <line x1="80" y1="65" x2="1120" y2="65" stroke="#1E293B" stroke-width="1"/>

    <line x1="80" y1="435" x2="1120" y2="435" stroke="#1E293B" stroke-width="1"/>
    <text x="80" y="455">CORE_VERSION: v2.4.0</text>
    <text x="600" y="455" text-anchor="middle">AGENT BUS STATE: NOMINAL · LEASE_TTL: 300S</text>
    <text x="1120" y="455" text-anchor="end">STATUS: SYNCED</text>
  </g>

  <!-- Chassis Corner Brackets -->
  <path d="M 70,85 L 70,75 L 95,75" stroke="#38BDF8" stroke-width="2" fill="none" opacity="0.6"/>
  <path d="M 1130,85 L 1130,75 L 1105,75" stroke="#38BDF8" stroke-width="2" fill="none" opacity="0.6"/>
  <path d="M 70,415 L 70,425 L 95,425" stroke="#38BDF8" stroke-width="2" fill="none" opacity="0.6"/>
  <path d="M 1130,415 L 1130,425 L 1105,425" stroke="#38BDF8" stroke-width="2" fill="none" opacity="0.6"/>

  <!-- MAIN WORDMARK -->
  <g id="wordmark-2" fill="url(#cyberCore)">
    <path d="{r_path}" fill-rule="evenodd"/>
    <path d="{a_path}"/>
    <path d="{v_path}"/>
    <path d="{n_path}"/>
    <!-- Cyan Inlays -->
    <rect x="268" y="172" width="3" height="45" fill="#38BDF8"/>
    <rect x="268" y="260" width="3" height="45" fill="#38BDF8"/>
    <rect x="466" y="253" width="72" height="3" fill="#38BDF8"/>
    <rect x="678.5" y="180" width="3" height="85" fill="#38BDF8" opacity="0.9"/>
    <rect x="793" y="172" width="3" height="45" fill="#38BDF8"/>
    <rect x="907" y="260" width="3" height="45" fill="#38BDF8"/>
  </g>

  <!-- Phosphor LED Cores -->
  <g filter="url(#cyanGlow)">
    <rect x="480" y="112" width="18" height="18" rx="3" fill="#38BDF8"/>
    <rect x="484" y="116" width="10" height="10" rx="1.5" fill="#FFFFFF"/>
  </g>
  <g filter="url(#cyanGlow)">
    <rect x="522" y="112" width="18" height="18" rx="3" fill="#38BDF8"/>
    <rect x="526" y="116" width="10" height="10" rx="1.5" fill="#FFFFFF"/>
  </g>

  <!-- Tagline -->
  <g font-family="ui-monospace, SFMono-Regular, Menlo, Monaco, monospace" font-size="11" fill="#38BDF8" text-anchor="middle" letter-spacing="0.3em">
    <text x="600" y="398">AUTONOMOUS MULTI-AGENT STATE MACHINE</text>
    <text x="600" y="416" font-size="9" fill="#64748B" letter-spacing="0.2em">[ PROOF-OF-DONE · ATOMIC CLAIMS · ZERO-DRIFT ]</text>
  </g>
</svg>'''
    with open("design-logos/logo-2-cyber-stencil.svg", "w") as f:
        f.write(svg)
    print("Saved logo-2-cyber-stencil.svg")

# -------------------------------------------------------------------------
# LOGO 3: APEX KINETIC / THE CORVID FLIGHT (Dynamic Aerodynamic)
# -------------------------------------------------------------------------
def build_logo_3():
    # 14° Forward Optical Italic
    # Balanced proportions, raven culmen hook, swept feather blades
    
    r_path = """
      M 250,320
      L 290,160
      L 362,160
      C 395,160 412,178 406,204
      C 400,230 380,244 348,244
      L 395,320
      L 367,320
      L 326,244
      L 290,244
      L 272,320
      Z
      M 295,224
      L 344,224
      C 366,224 376,216 380,204
      C 384,192 374,180 352,180
      L 306,180
      Z
    """

    a_path = """
      M 520,160
      L 580,320
      L 552,320
      L 538,280
      L 472,280
      L 442,320
      L 414,320
      L 496,160
      Z
      M 510,198
      L 483,260
      L 531,260
      Z
    """

    v_path = """
      M 615,160
      L 641,160
      L 675,280
      L 741,160
      L 769,160
      L 687,320
      L 661,320
      Z
    """

    n_path = """
      M 795,160
      L 819,160
      L 887,288
      L 919,160
      L 943,160
      L 903,320
      L 879,320
      L 811,192
      L 779,320
      L 755,320
      Z
    """

    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 500" width="1200" height="500" role="img" aria-label="Rävn Logotype - Apex Kinetic Corvid Wing">
  <title>Rävn - Logotype 03: Apex Kinetic Corvid Wing</title>
  <defs>
    <linearGradient id="bg3" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#0F0C1B"/>
      <stop offset="50%" stop-color="#08070F"/>
      <stop offset="100%" stop-color="#040308"/>
    </linearGradient>
    <linearGradient id="plumageGrad" x1="0%" y1="0%" x2="100%" y2="50%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="30%" stop-color="#E0E7FF"/>
      <stop offset="70%" stop-color="#C7D2FE"/>
      <stop offset="100%" stop-color="#A5B4FC"/>
    </linearGradient>
    <linearGradient id="wingtipAmber" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#F43F5E"/>
      <stop offset="45%" stop-color="#FB923C"/>
      <stop offset="100%" stop-color="#FBBF24"/>
    </linearGradient>
    <radialGradient id="kineticAura" cx="48%" cy="50%" r="55%">
      <stop offset="0%" stop-color="#6366F1" stop-opacity="0.18"/>
      <stop offset="45%" stop-color="#8B5CF6" stop-opacity="0.08"/>
      <stop offset="100%" stop-color="#08070F" stop-opacity="0"/>
    </radialGradient>
    <filter id="softBladeShadow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="3" dy="6" stdDeviation="8" flood-color="#000000" flood-opacity="0.6"/>
    </filter>
  </defs>

  <rect width="1200" height="500" fill="url(#bg3)"/>
  <rect width="1200" height="500" fill="url(#kineticAura)"/>

  <!-- Dynamic Aerodynamic Flow Lines -->
  <g opacity="0.15" stroke="#818CF8" stroke-width="1.2">
    <path d="M 80,180 C 300,160 800,140 1120,130" stroke-dasharray="6 12"/>
    <path d="M 120,250 C 350,240 850,225 1160,210" stroke-dasharray="10 16"/>
    <path d="M 60,330 C 280,325 780,315 1100,300" stroke-dasharray="8 14"/>
  </g>

  <!-- Header Label -->
  <g font-family="ui-monospace, SFMono-Regular, Menlo, Monaco, monospace" font-size="10" fill="#6366F1" letter-spacing="0.3em">
    <text x="120" y="80">AERODYNAMIC PROFILE // KINETIC VECTOR</text>
    <text x="1080" y="80" text-anchor="end" fill="#FB923C">RAKE ANGLE: 14° // CORVID FORMWORK</text>
  </g>

  <!-- MAIN WORDMARK -->
  <g id="wordmark-3" filter="url(#softBladeShadow)" fill="url(#plumageGrad)">
    <path d="{r_path}" fill-rule="evenodd"/>
    <path d="{a_path}" fill-rule="evenodd"/>
    <path d="{v_path}"/>
    <path d="{n_path}"/>
    <!-- Kinetic Amber Accents on Wingtips -->
    <polygon points="380,314 402,320 395,320 367,320" fill="url(#wingtipAmber)"/>
    <polygon points="677,314 692,320 687,320 661,320" fill="url(#wingtipAmber)"/>
    <polygon points="943,160 953,164 939,172" fill="url(#wingtipAmber)"/>
  </g>

  <!-- Swept Feather Quills for Umlaut -->
  <polygon points="504,114 522,114 504,136 488,136" fill="url(#wingtipAmber)"/>
  <polygon points="534,114 552,114 534,136 518,136" fill="url(#wingtipAmber)"/>

  <!-- Kinetic Baseline Bar -->
  <line x1="220" y1="345" x2="980" y2="345" stroke="#312E81" stroke-width="1.5" stroke-dasharray="12 8"/>
  <rect x="220" y="344" width="40" height="3.5" fill="#FB923C"/>
  <circle cx="980" cy="345" r="3" fill="#FB923C"/>

  <!-- Footer Brand Signature -->
  <g font-family="system-ui, -apple-system, sans-serif" text-anchor="middle">
    <text x="600" y="415" font-size="13" font-weight="700" fill="#E0E7FF" letter-spacing="0.4em">RÄVN · THE FLIGHT ENGINE FOR AGENTS</text>
    <text x="600" y="435" font-size="10" font-weight="500" fill="#818CF8" letter-spacing="0.25em">HIGH VELOCITY · PERSISTENT MEMORY · DUAL TRANSPORT</text>
  </g>
</svg>'''
    with open("design-logos/logo-3-apex-kinetic.svg", "w") as f:
        f.write(svg)
    print("Saved logo-3-apex-kinetic.svg")

# -------------------------------------------------------------------------
# LOGO 4: BRUTALIST MONUMENT / ARCHITECTURAL SLAB (Swiss Constructivism)
# -------------------------------------------------------------------------
def build_logo_4():
    # 950 Monumental Heavy weight (stem = 44px)
    # Baseline: 330, Cap: 150 (Height: 180)
    
    r_path = """
      M 190,330
      L 190,150
      L 315,150
      C 355,150 375,172 375,204
      C 375,236 355,250 315,250
      L 380,330
      L 332,330
      L 282,250
      L 234,250
      L 234,330
      Z
      M 234,188
      L 305,188
      C 320,188 331,194 331,204
      C 331,214 320,220 305,220
      L 234,220
      Z
    """

    a_path = """
      M 505,150
      L 600,330
      L 552,330
      L 535,284
      L 475,284
      L 458,330
      L 410,330
      Z
      M 505,200
      L 485,250
      L 525,250
      Z
    """

    v_path = """
      M 625,150
      L 673,150
      L 715,268
      L 757,150
      L 805,150
      L 738,330
      L 692,330
      Z
    """

    n_path = """
      M 830,150
      L 874,150
      L 951,296
      L 951,150
      L 995,150
      L 995,330
      L 951,330
      L 874,184
      L 874,330
      L 830,330
      Z
    """

    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 500" width="1200" height="500" role="img" aria-label="Rävn Logotype - Monumental Swiss Brutalist">
  <title>Rävn - Logotype 04: Monumental Swiss Brutalist</title>
  <defs>
    <linearGradient id="bg4" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#121214"/>
      <stop offset="100%" stop-color="#080809"/>
    </linearGradient>
    <linearGradient id="swissOrange" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#FF453A"/>
      <stop offset="100%" stop-color="#FF3B30"/>
    </linearGradient>
    <linearGradient id="slabWhite" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" stop-color="#FFFFFF"/>
      <stop offset="100%" stop-color="#EDEDED"/>
    </linearGradient>
    <filter id="brutalistShadow" x="-10%" y="-10%" width="120%" height="130%">
      <feDropShadow dx="0" dy="12" stdDeviation="16" flood-color="#000000" flood-opacity="0.8"/>
    </filter>
  </defs>

  <rect width="1200" height="500" fill="url(#bg4)"/>

  <!-- Swiss Modular Baseline Grid -->
  <g opacity="0.12" stroke="#FFFFFF" stroke-width="0.75">
    <line x1="120" y1="50" x2="1080" y2="50"/>
    <line x1="120" y1="150" x2="1080" y2="150"/>
    <line x1="120" y1="240" x2="1080" y2="240"/>
    <line x1="120" y1="330" x2="1080" y2="330"/>
    <line x1="120" y1="420" x2="1080" y2="420"/>
  </g>

  <!-- International Typographic Style Header Info (Clear of Umlauts) -->
  <g font-family="Helvetica Neue, Helvetica, Arial, sans-serif" font-weight="700" font-size="11" fill="#71717A" letter-spacing="0.15em">
    <text x="140" y="75">RAVN / SYSTEMATISCHE TYPOGRAFIE</text>
    <text x="600" y="75" text-anchor="middle">STUFE 950 : MONUMENTAL SCHWER</text>
    <text x="1060" y="75" text-anchor="end" fill="#FF453A">FIG. 04 · SWISS GEOMETRIC</text>
  </g>

  <!-- Red Swiss Safety Accent Bar -->
  <rect x="140" y="86" width="920" height="3" fill="url(#swissOrange)"/>

  <!-- MAIN WORDMARK: R Ä V N (Monumental Slab Geometry) -->
  <g id="wordmark-4" filter="url(#brutalistShadow)" fill="url(#slabWhite)">
    <path d="{r_path}" fill-rule="evenodd"/>
    <path d="{a_path}" fill-rule="evenodd"/>
    <path d="{v_path}"/>
    <path d="{n_path}"/>
  </g>

  <!-- UMLAUT: Twin Solid Monumental Circular Discs (Signal Vermilion) -->
  <circle cx="482" cy="116" r="14" fill="url(#swissOrange)"/>
  <circle cx="528" cy="116" r="14" fill="url(#swissOrange)"/>

  <!-- Bottom Swiss Specification Rule -->
  <line x1="140" y1="365" x2="1060" y2="365" stroke="#27272A" stroke-width="1.5"/>

  <!-- Technical Specimen Captions -->
  <g font-family="Helvetica Neue, Helvetica, Arial, sans-serif" font-size="11" fill="#A1A1AA">
    <text x="140" y="392" font-weight="700">RÄVN INFRASTRUCTURE</text>
    <text x="140" y="410" fill="#71717A">KUBERNETES &amp; DOCKER NATIVE · ONION ARCHITECTURE</text>
    
    <text x="600" y="392" text-anchor="middle" font-weight="700">PURE GO SQLITE</text>
    <text x="600" y="410" text-anchor="middle" fill="#71717A">ZERO CGO DEPENDENCIES · STATIC BINARY</text>

    <text x="1060" y="392" text-anchor="end" font-weight="700" fill="#FF453A">100% VECTOR PATHS</text>
    <text x="1060" y="410" text-anchor="end" fill="#71717A">DESIGN RATIO: 1:1.618</text>
  </g>
</svg>'''
    with open("design-logos/logo-4-brutalist-slab.svg", "w") as f:
        f.write(svg)
    print("Saved logo-4-brutalist-slab.svg")

if __name__ == "__main__":
    build_logo_1()
    build_logo_2()
    build_logo_3()
    build_logo_4()
