#
# ===========================================================================
# pet_item_icons.py - export owned pet gear artwork to native32px item icons.
#
# Each source atlas is3x3, degrees1..9 in row order. Names and item bindings
# remain unchanged; the normal publisher owns all generated outputs.
# ===========================================================================
#
import base64
import io
import json
from pathlib import Path

from PIL import Image


#
# ================
# export_icons
# ================
#
def export_icons():
	icons = []
	sources = Path(__file__).resolve().parents[3] / "docs/pet-expansion/assets"
	for slot in ("collar", "harness", "charm"):
		with Image.open(sources / f"pet-gear-{slot}.png") as source:
			atlas = source.convert("RGBA")
			width, height = atlas.size
			if width != height or width < 96:
				raise ValueError(f"Pet {slot} icons need a square3x3 source atlas")
			for degree in range(1, 10):
				row, col = divmod(degree - 1, 3)
				bounds = (round(col * width / 3), round(row * height / 3),
					round((col + 1) * width / 3), round((row + 1) * height / 3))
				icon = atlas.crop(bounds).resize((32, 32), Image.Resampling.LANCZOS)
				output = io.BytesIO()
				icon.save(output, format="PNG")
				icons.append({"slot": slot, "degree": degree, "png": base64.b64encode(output.getvalue()).decode("ascii")})
	return icons


if __name__ == "__main__":
	print(json.dumps(export_icons()))
