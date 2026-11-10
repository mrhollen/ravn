"""
Master generator for Swiss Brutalist logotypes:
- Exact uniform stroke width across every letter (verticals, diagonals, crossbars, curves)
- ZERO decorative elements (the project name ONLY)
- 100% transparent background
- Pure solid vector paths (available in White for dark backgrounds and Black for light backgrounds)
"""
import math
import os
import shutil

os.makedirs("design-logos", exist_ok=True)

def generate_brutalist_variant(W=32.0, H=160.0, W_cap_ratio=0.875, square_umlaut=False, base_filename="logo-brutalist-1-standard"):
    Y_top = 56.0
    Y_base = Y_top + H  # 216.0
    
    # -------------------------------------------------------------
    # 1. LETTER R (X: 30 to 154)
    # -------------------------------------------------------------
    X_r = 30.0
    bowl_h = 88.0
    W_r_bowl = 104.0
    W_r_total = 124.0
    
    dy_leg = H - bowl_h  # 72.0
    dx_leg_span = W_r_total - 76.0  # 48.0
    theta_leg = math.atan2(dy_leg, dx_leg_span)
    dx_leg = W / math.sin(theta_leg)
    
    r_path = f"""
      M {X_r},{Y_base}
      L {X_r},{Y_top}
      L {X_r + 56},{Y_top}
      C {X_r + W_r_bowl},{Y_top} {X_r + W_r_bowl},{Y_top + bowl_h} {X_r + 76},{Y_top + bowl_h}
      L {X_r + W_r_total},{Y_base}
      L {X_r + W_r_total - dx_leg:.3f},{Y_base}
      L {X_r + 76 - dx_leg:.3f},{Y_top + bowl_h}
      L {X_r + W},{Y_top + bowl_h}
      L {X_r + W},{Y_base}
      Z
      M {X_r + W},{Y_top + W}
      L {X_r + 56},{Y_top + W}
      C {X_r + W_r_bowl - W},{Y_top + W} {X_r + W_r_bowl - W},{Y_top + bowl_h - W} {X_r + 76 - W},{Y_top + bowl_h - W}
      L {X_r + W},{Y_top + bowl_h - W}
      Z
    """

    # -------------------------------------------------------------
    # 2. LETTER Ä (X: 174 to 314, Width = 140)
    # -------------------------------------------------------------
    X_a_left = 174.0
    W_a = 140.0
    X_a_right = X_a_left + W_a  # 314
    X_center = X_a_left + W_a / 2.0  # 244
    W_cap = W * W_cap_ratio
    
    half_span = (W_a - W_cap) / 2.0
    theta_a = math.atan2(H, half_span)
    sin_a = math.sin(theta_a)
    tan_a = math.tan(theta_a)
    dx_a = W / sin_a
    
    Y_inner_apex = Y_top + (dx_a - W_cap / 2.0) * tan_a
    
    Y_bar_top = 138.0
    Y_bar_bot = Y_bar_top + W
    
    x_c_left = (X_center - W_cap/2.0) - (Y_bar_top - Y_top)/tan_a + dx_a
    x_c_right = (X_center + W_cap/2.0) + (Y_bar_top - Y_top)/tan_a - dx_a
    
    x_p_left = (X_center - W_cap/2.0) - (Y_bar_bot - Y_top)/tan_a + dx_a
    x_p_right = (X_center + W_cap/2.0) + (Y_bar_bot - Y_top)/tan_a - dx_a
    x_foot_left = X_a_left + dx_a
    x_foot_right = X_a_right - dx_a
    
    a_path = f"""
      M {X_center - W_cap/2.0:.3f},{Y_top}
      L {X_center + W_cap/2.0:.3f},{Y_top}
      L {X_a_right},{Y_base}
      L {x_foot_right:.3f},{Y_base}
      L {x_p_right:.3f},{Y_bar_bot}
      L {x_p_left:.3f},{Y_bar_bot}
      L {x_foot_left:.3f},{Y_base}
      L {X_a_left},{Y_base}
      Z
      M {X_center},{Y_inner_apex:.3f}
      L {x_c_left:.3f},{Y_bar_top}
      L {x_c_right:.3f},{Y_bar_top}
      Z
    """

    # Umlaut:
    Y_umlaut_center = Y_top - W/2.0 - 12.0
    X_u1 = X_center - W * 0.7
    X_u2 = X_center + W * 0.7
    
    if square_umlaut:
        umlaut_svg = f"""
      <rect x="{X_u1 - W/2.0:.3f}" y="{Y_top - W - 12.0:.3f}" width="{W}" height="{W}"/>
      <rect x="{X_u2 - W/2.0:.3f}" y="{Y_top - W - 12.0:.3f}" width="{W}" height="{W}"/>
        """
    else:
        umlaut_svg = f"""
      <circle cx="{X_u1:.3f}" cy="{Y_umlaut_center:.3f}" r="{W/2.0:.3f}"/>
      <circle cx="{X_u2:.3f}" cy="{Y_umlaut_center:.3f}" r="{W/2.0:.3f}"/>
        """

    # -------------------------------------------------------------
    # 3. LETTER V (X: 334 to 474, Width = 140)
    # -------------------------------------------------------------
    X_v_left = 334.0
    W_v = W_a
    X_v_right = X_v_left + W_v  # 474
    X_v_center = X_v_left + W_v / 2.0  # 404
    W_plateau = W_cap
    
    Y_v_inner = Y_base - (dx_a - W_plateau / 2.0) * tan_a
    
    v_path = f"""
      M {X_v_left},{Y_top}
      L {X_v_left + dx_a:.3f},{Y_top}
      L {X_v_center},{Y_v_inner:.3f}
      L {X_v_right - dx_a:.3f},{Y_top}
      L {X_v_right},{Y_top}
      L {X_v_center + W_plateau/2.0:.3f},{Y_base}
      L {X_v_center - W_plateau/2.0:.3f},{Y_base}
      Z
    """

    # -------------------------------------------------------------
    # 4. LETTER N (X: 494 to 612, Width = 118)
    # -------------------------------------------------------------
    X_n_left = 494.0
    W_n = 118.0
    X_n_right = X_n_left + W_n  # 612
    
    theta_n = math.atan2(H, W_n)
    sin_n = math.sin(theta_n)
    dx_n = W / sin_n
    tan_n = math.tan(theta_n)
    
    y_diag_left = Y_top + tan_n * W
    y_diag_right = Y_top + tan_n * (W_n - W - dx_n)
    
    n_path = f"""
      M {X_n_left},{Y_top}
      L {X_n_left + dx_n:.3f},{Y_top}
      L {X_n_right - W},{y_diag_right:.3f}
      L {X_n_right - W},{Y_top}
      L {X_n_right},{Y_top}
      L {X_n_right},{Y_base}
      L {X_n_right - dx_n:.3f},{Y_base}
      L {X_n_left + W},{y_diag_left:.3f}
      L {X_n_left + W},{Y_base}
      L {X_n_left},{Y_base}
      Z
    """

    # Function to build SVG string with specific color
    def make_svg(fill_color):
        return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 642 236" width="642" height="236" role="img" aria-label="Rävn">
  <title>Rävn</title>
  <g fill="{fill_color}" fill-rule="evenodd" transform="translate(15, 10)">
    <!-- R -->
    <path d="{r_path}"/>
    <!-- Ä -->
    <path d="{a_path}"/>
    <!-- UMLAUT -->
    {umlaut_svg}
    <!-- V -->
    <path d="{v_path}"/>
    <!-- N -->
    <path d="{n_path}"/>
  </g>
</svg>'''

    # Primary (White for dark backgrounds)
    with open(f"design-logos/{base_filename}.svg", "w") as f:
        f.write(make_svg("#FFFFFF"))
    # Light surface version (Dark Obsidian text)
    with open(f"design-logos/{base_filename}-light.svg", "w") as f:
        f.write(make_svg("#09090B"))
    print(f"Generated {base_filename}.svg and {base_filename}-light.svg")

# Generate all 4 brutalist variations
generate_brutalist_variant(W=32.0, W_cap_ratio=0.875, square_umlaut=False, base_filename="logo-brutalist-1-standard")
generate_brutalist_variant(W=36.0, W_cap_ratio=0.85, square_umlaut=False, base_filename="logo-brutalist-2-heavy")
generate_brutalist_variant(W=28.0, W_cap_ratio=0.9, square_umlaut=False, base_filename="logo-brutalist-3-precision")
generate_brutalist_variant(W=32.0, W_cap_ratio=0.875, square_umlaut=True, base_filename="logo-brutalist-4-block")

# Update logo-4-brutalist-slab.svg to be Option 1
shutil.copyfile("design-logos/logo-brutalist-1-standard.svg", "design-logos/logo-4-brutalist-slab.svg")
shutil.copyfile("design-logos/logo-brutalist-1-standard-light.svg", "design-logos/logo-4-brutalist-slab-light.svg")
print("Updated logo-4-brutalist-slab.svg")
