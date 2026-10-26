# OpenSRO & New-Silkroad Güncelleme (`.temir`) ve Linux Deploy Kılavuzu

Bu doküman; **New-Silkroad (`Silkroad.exe`)** masaüstü istemcisinin `.temir` arşiv mimarisini, otomatik güncelleme (auto-update) ve yerinde birleştirme (in-place merge) çalışma prensibini, ilk kurulum davranışını ve **OpenSRO Linux Sunucu (`linux-server`)** dağıtım (`deploy_linux.py`) iş akışını açıklar.

---

## 1. `.temir` Arşiv Mimarisi ve Dosya Dağılımı

İstemci tüm oyun verilerini ve `client-next` (WebGPU/TypeScript) ön yüzünü şifreli 3 ana `.temir` konteynerinde tutar. Her sanal dosya yolu (`virtual_path`), `classify_virtual_path` / `AssignArchive` kurallarına göre otomatik olarak ilgili arşive yönlendirilir:

| Arşiv Adı | İçerdiği Dosya Yolları | Açıklama |
| --- | --- | --- |
| **`Media.temir`** | `index.html`, `main.js`, `styles.css`, `assets/bootstrap-*.js`, `assets/entry-*.js`, `assets/index-*.css`, `assets/*.wasm`, `assets/packs/manifest.json*`, `assets/packs/interface*`, `assets/delivery/*`, `assets/images/*` | **`client-next` motor/UI kodlarının tamamı**, masaüstü kabuk dosyaları, tablo verileri ve kullanıcı arayüzü görselleri |
| **`Data.temir`** | `assets/packs/char-*`, `assets/packs/item-*`, `assets/packs/mob-*`, `assets/packs/obj-*`, `assets/packs/equipment-models-*`, `assets/packs/game-models-*`, `assets/packs/hwan-models-*`, `assets/packs/mission-cos-models-*`, `assets/packs/mission-npc-vat-*`, `assets/packs/title-crowd-vat-*`, `assets/packs/texture-variants-*` | Karakter, ekipman, canavar, binek/pet (COS), NPC modelleri ve ASTC/DDS dokuları |
| **`World.temir`** | `assets/packs/map-*`, `assets/packs/outdoor-world-*`, `assets/packs/mission-minimap-*`, `assets/packs/world-textures-*`, `assets/packs/game-audio-*`, `assets/packs/snd-*`, `assets/packs/particle-*`, `assets/packs/fx-*` | Harita bölgeleri, arazi (terrain), minimap, oyun içi müzik/ses ve partikül efektleri |

---

## 2. Gömülü `server.json` ve İlk Kurulum Davranışı

`Silkroad.exe` içerisine varsayılan sunucu yapılandırması doğrudan gömülmüştür (`src-tauri/src/lib.rs` -> `EMBEDDED_SERVER_JSON`):

```json
{
	"agent": "https://sro.ftemir.com",
	"maxClients": 0,
	"updateUrl": "https://sro.ftemir.com/updates"
}
```

### Yapılandırma Öncelik Sırası
1. **`Silkroad.exe` ile aynı klasörde `server.json` varsa:** Diskteki `server.json` okunur ve gömülü varsayılanların üzerine yazılır (yerel test veya farklı bir sunucuya bağlanmak için kullanılır).
2. **Aynı klasörde `server.json` yoksa:** Doğrudan gömülü varsayılan ayarlar kullanılır. Oyuncuya sadece tek bir `Silkroad.exe` dosyası verilmesi yeterlidir.

### Sıfırdan İlk Kurulum (`last_version/` Klasörü Üzerinden Tek Seferde İndirme)
1. Sunucuda (`linux-server/last_version/`) her zaman en güncel yamaların içine gömülmüş olduğu **`Media.temir`**, **`Data.temir`** ve **`World.temir`** dosyaları tutulur.
2. Oyuncu boş bir klasörde sadece `Silkroad.exe` dosyasını çalıştırır.
3. İstemci `Media.temir`, `Data.temir` ve `World.temir` arşivlerinin klasörde olmadığını tespit eder ve önyükleme (bootstrap) indirme ekranını açar.
4. `https://sro.ftemir.com/updates/patches.json` adresinden manifest dosyasını çeker.
5. İlk kurulum olduğu için doğrudan `https://sro.ftemir.com/updates/last_version/Media.temir`, `Data.temir` ve `World.temir` dosyalarını indirir.
6. Sunucu tüm `PatchNNN.temir` yamalarını zaten `last_version/` içindeki ana arşivlere gömmüş olduğu için, ilk kez kuran oyuncu **hiçbir ek `PatchNNN.temir` dosyası indirmeden** doğrudan son sürümle oyunu başlatır.

---

## 3. Günlük Güncellemeler ve Yerinde Birleştirme (In-Place Merge)

Oyun dosyalarında veya `client-next` kodunda değişiklik yaptığınızda ne sunucuya tekrar 2 GB dosya yüklenir ne de oyuncunun klasöründe `Patch001.temir`, `Patch002.temir` gibi yama dosyaları birikir.

### Nasıl Çalışır?
1. **Sunucu Tarafı (`sync.go` & `merge.go`):**
   - Sunucu, `updates/baseline.json` (veya mevcut yamalar) ile `client-dist/` + `client-public/` dosyalarını karşılaştırır.
   - Sadece değişen dosyaları `updates/PatchNNN.temir` içine paketler.
   - **Otomatik Sunucu Birleştirmesi (`MergePatchIntoBaseArchives`):** Sunucu yeni üretilen veya yüklenen `PatchNNN.temir` dosyasını anında kendi diskindeki **`last_version/Media.temir`**, **`last_version/Data.temir`** ve **`last_version/World.temir`** dosyalarının içine yerinde (in-place) gömer.
   - Böylece mevcut oyuncular için küçük `updates/PatchNNN.temir` korunurken, yeni oyuncular için `last_version/*.temir` her zaman en güncel halde kalır.
2. **İstemci Tarafı (`sropk.rs` & `updater.rs`):**
   - Oyuncu `Silkroad.exe`'yi açtığında `patches.json` içindeki `entries` listesini bellekteki (`VirtualFileSystem`) `Media.temir`, `Data.temir` ve `World.temir` TOC kayıtlarıyla `< 0.01 ms` içinde karşılaştırır.
   - Eğer yamadaki dosyalar yerel arşivlerde henüz güncel değilse `PatchNNN.temir` indirilir.
   - `absorb_patch_into_base_archives` fonksiyonu yamanın içindeki her dosyayı sanal yoluna göre `Media.temir`, `Data.temir` veya `World.temir` hedefine ayırır:
     - Dosyanın yeni boyutu eski boyutuna eşit veya küçükse (`new_len <= old_len`), arşiv içindeki mevcut `data_offset` adresinin **tam üzerine** yazar (dosya boyutu büyümez).
     - `client-next` derlemesinde yeni bir `assets/bootstrap-<yeni_hash>.js` veya `assets/index-<yeni_hash>.css` geldiğinde, `Media.temir` içindeki eski hash'li `bootstrap-*.js` / `index-*.css` kaydını TOC'dan siler ve boşalan alanı yeniden kullanır.
     - Dosya büyümüşse veya yeni eklenmişse arşivin sonuna (`old_toc_offset`) yazar, şifreli TOC tablosunu ve 64 baytlık başlığı günceller.
   - Birleştirme tamamlandığında indirilen `PatchNNN.temir` dosyası **anında silinir**.
   - Oyuncunun klasöründe her zaman yalnızca `Media.temir`, `Data.temir` ve `World.temir` kalır.

---

## 4. Rutin Geliştirme ve Sunucuya Deploy Komutları

Uzak sunucu bağlantı bilgileri `linux-server/deploy.json` ve git-ignored `deploy.local.json` dosyalarında tanımlıdır:
- **Sunucu:** `51.255.55.181` (`sro.ftemir.com`)
- **Kullanıcı:** `ubuntu1`
- **Uzak Dizin:** `/home/ubuntu1/Desktop/opensro`

### Senaryo A: `client-next` (UI / Motor) Değişikliği Yaptığınızda
```powershell
# 1. client-next üretim derlemesini alın:
corepack pnpm --filter @sro/client-next build

# 2. Değişiklikleri paketleyip sunucuya gönderin:
python apps/server/ops/release/deploy_linux.py
```
> **Not:** Sadece istemci yamalarını (`updates/` ve `client-dist/`) göndermek ve Go sunucularını yeniden derlememek isterseniz:
> ```powershell
> python apps/server/ops/release/deploy_linux.py --only-updates
> ```

### Senaryo B: Go Sunucu (`agent` / `gameworld` / `panel`) Değişikliği Yaptığınızda
```powershell
# Tüm Linux binary'lerini derler, değişenleri SHA-256 ile tespit edip yükler ve servisleri yeniden başlatır:
python apps/server/ops/release/deploy_linux.py
```

---

## 5. Çok Sayıda Yama Biriktiğinde Sıfırlama (Baz Arşivleri Yenileme)

Zamanla sunucunun `updates/` klasöründe çok sayıda `Patch001.temir .. Patch020.temir` biriktiğinde, tüm yamaları temizleyip güncel `Media.temir`, `Data.temir` ve `World.temir` baz arşivlerini yayınlamak için:

### Adım 1: Yerelde Güncel Baz Arşivleri Üretin
```powershell
# Sadece Media.temir değiştiyse (hızlı, ~2 sn):
C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\new-silkroad\sro-packer.exe pack --only Media.temir --assets C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\opensro\.generated\client-public

# Veya 3 ana arşivin tamamını (Media, Data, World) baştan paketlemek için:
C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\new-silkroad\sro-packer.exe pack --assets C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\opensro\.generated\client-public
```

### Adım 2: `linux-server/updates/` İçindeki Eski Yamaları Silip Yeni Baz Arşivleri Koyun
```powershell
# 1. Eski Patch*.temir dosyalarını silin:
Remove-Item "C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\linux-server\updates\Patch*.temir" -Force

# 2. Güncel Media.temir, Data.temir, World.temir dosyalarını linux-server/updates/ içine bağlayın/kopyalayın:
python -c "import os; src = r'C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\new-silkroad'; dst = r'C:\Users\DELL\Desktop\VSRODevelopment\EYLUL2026\linux-server\updates'; [os.remove(os.path.join(dst, n)) for n in ('Media.temir', 'Data.temir', 'World.temir') if os.path.exists(os.path.join(dst, n))]; [os.link(os.path.join(src, n), os.path.join(dst, n)) for n in ('Media.temir', 'Data.temir', 'World.temir')]"

# 3. Manifest ve baseline.json'ı güncelleyip sunucuya gönderin:
python apps/server/ops/release/deploy_linux.py --only-updates
```

### Bu İşlem Sonrasında Ne Olur?
- Sunucudaki `sync.go` (`backfillPatchEntries`), `Patch*.temir` dosyalarının silindiğini ve `Media.temir` / `Data.temir` / `World.temir` başlık imzalarının (`headerTag`) yenilendiğini otomatik algılar.
- `baseline.json` yeni baz arşivlere göre sıfırlanır ve `patches.json` versiyonu artırılır.
- Uzak sunucudaki (`51.255.55.181`) eski `Patch*.temir` dosyalarını da temizlemek için:
  ```powershell
  ssh ubuntu1@51.255.55.181 "printf '123\n' | sudo -S rm -f /home/ubuntu1/Desktop/opensro/updates/Patch*.temir"
  ```
- Oyuncular oyunu açtığında yalnızca `headerTag` imzası değişen ana arşivi (örneğin sadece `Media.temir` yenilendiyse sadece `Media.temir`'i) indirip günceller; değişmeyen `Data.temir` ve `World.temir` arşivlerini tekrar indirmez.

