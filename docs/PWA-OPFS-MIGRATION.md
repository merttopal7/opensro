# PWA + OPFS Geçişi: `.temir` Arşivlerini Tarayıcıda Yerel Okumak

Bu doküman, masaüstü Tauri istemcisinin (new-silkroad) yaptığı işi, yani oyun
verisini yerel `.temir` arşivlerinden okumayı, kurulu bir PWA'ya taşıma planıdır.
Okuyucu: bu fork üzerinde çalışan geliştiriciler ve kodlama ajanları.
`.temir` formatı ve sunucudaki güncelleme akışı için önce
[UPDATE-AND-DEPLOY.md](UPDATE-AND-DEPLOY.md) okunmalı.

Durum: **A0, A1, A2, A3, A3b, A5 ve A6 tamamlandı.** Kurulu PWA ilk
açılışta arşivleri OPFS'e indiriyor, yarıda kesilirse kaldığı yerden
sürdürüyor ve oyunu ağdan hiçbir pack istemeden OPFS'ten açıyor (uçtan uca
doğrulandı). Sunucu güncellenince yalnızca gereken patch'leri indirip temel
arşivlerin üstüne bindiriyor (A6). `/updates/` geçici olarak erişilemezse
eldeki arşivlerle açılıyor (A5). 4 worker'lı OPFS okuma, Tauri'den ~1,7–2
kat hızlı (9. bölüm). Kalanlar: **A8** gerçek cihaz matrisi (iOS, macOS,
Android), yayın ve isteğe bağlı iyileştirmeler (paralel okuma worker'ları,
A7 yerinde birleştirme).

---

## 1. Amaç

- Oyuncu kurulum yapmadan (mağaza veya `.exe` olmadan) oyunu tarayıcıdan
  "uygulama" olarak kurar. İlk açılışta `.temir` arşivleri cihaza iner, sonra
  oyun Tauri'deki gibi **yerelden** okur.
- Okuma hızı Tauri ile **başa baş** olmalı. Yeni bölgeye girişte ağ beklemesi
  olmamalı.
- Tauri istemcisi asıl masaüstü istemcisi olarak **kalır**. PWA onun yerine
  geçmez, yanına eklenir.

Kapsam dışı: oyun içi davranış değişikliği, render, sunucu protokolü.

## 2. Bugünkü durum

İstemcinin (`apps/client-next`) bugün iki veri yolu var. Hangisinin
kullanılacağına user agent'taki `OpenSRODesktop/` işareti karar veriyor
(`desktopShell` / `DESKTOP_SHELL`).

| | Tauri (masaüstü) | Tarayıcı (bugünkü web) |
|---|---|---|
| Veri | Diskte `Media/Data/World.temir` (~5,6 GB) | Ağ + Cache Storage |
| Okuma | WebView2 → `127.0.0.1:15180` → Rust → ChaCha20 | worker → `caches.match` |
| Okumada SHA-256 | Yok | Her okumada (`packs.ts`) |
| Eşzamanlı istek | 12 | 4 (`assets.ts`, `loader.ts`) |
| Kalıcı depo sınırı | Disk | En çok 4 GiB, LRU (`persistent.ts`) |

Sunucu tarafı PWA için zaten hazır:

- Agent `/updates/` altında `patches.json`, `PatchNNN.temir` ve
  `last_version/*.temir` dosyalarını sunuyor
  (`apps/server/internal/agent/updates/handler.go`).
  `http.ServeContent` kullandığı için **Range** ve **If-Range** destekleniyor.
- Agent yanıtları CORS sarmalayıcısından geçiyor (`server.cors`).

## 3. Hedef mimari

```
Tarayıcı sekmesi ──► Kurulum kapısı: Chrome/Edge "Yükle", iOS/macOS rehber   (oyun yüklenmez)

Kurulu PWA:
  bootstrap (sunucudan; küçük kabuk + service worker)
     1. WebGPU kontrolü, navigator.storage.persist(), storage.estimate()
     2. patches.json → last_version/*.temir veya PatchNNN.temir → OPFS
     3. oyun kabuğunu (index.html, JS, CSS) OPFS'teki Media.temir'den sunar
     ▼
  oyun ─► asset worker ─► PackDownload (yerel arşiv kaynağı)
                              └─► temir.ts (format) ─► OPFS SyncAccessHandle
```

Tauri'de Rust'ın yaptığı her işin bir tarayıcı karşılığı var:

| Tauri (Rust, new-silkroad) | PWA |
|---|---|
| `sropk.rs` okuma tarafı | `src/engine/foundation/assets/temir.ts` (**A0, hazır**) |
| `ArchiveFilePool` + `read_exact_at` | OPFS `FileSystemSyncAccessHandle.read( buf, { at } )` (A3) |
| `updater.rs` indirme/patch | asset worker içinde indirici (A3, A6) |
| `serve_local_file` + `BOOTSTRAP_INSTALLER_HTML` | service worker + bootstrap sayfası (A5) |
| Gömülü `server.json` | PWA manifest + derleme sabiti |

## 4. Kararlar

| # | Karar | Gerekçe |
|---|---|---|
| K1 | Depolama **OPFS**, erişim **SyncAccessHandle** (dedicated worker) | En hızlı tarayıcı depolaması. İzin sorusu yok. Chrome, Edge, Firefox ve Safari'de var. Yerinde yazma Rust'taki birleştirme modeliyle aynı. |
| K2 | Arşiv formatı **değişmez** (`.temir` v1) | Sunucu, Tauri ve PWA aynı dosyaları kullanır. Sunucuda yeni bir üretim hattı gerekmez. |
| K3 | Okuyucu **TypeScript** (WASM değil), Go packer'ın ürettiği ortak bir fixture ile doğrulanır | opensro derlemesinde Rust araç zinciri yok. Format küçük. Ölçülen hız (V8): ChaCha20 **~245 MB/s**, anahtar türetme **~1,3 ms**. A1'de darboğaz çıkarsa yalnızca `chachaXorAt` WASM SIMD'e taşınır, API aynı kalır. |
| K4 | Oyun yalnızca **kurulu PWA**'da çalışır. Sekmede kurulum kapısı gösterilir. | iOS'ta sekme ile kurulu uygulamanın deposu ayrı ve Safari sekmesinde 7 gün kuralı var. Kurulu uygulamada süreye bağlı silme yok. |
| K5 | Yerel arşiv modu bir **yetenek** olur, UA kontrolü değil | Bugünkü `desktopShell` bayrağı "Cache Storage ve SHA'yı atla, bütçeleri yükselt" demek. Tauri ve OPFS ikisi de bu yeteneği açar. |
| K6 | Patch'ler önce **overlay** (ayrı dosya), sonra **yerinde birleştirme** | Overlay, A6'yı hızlı teslim eder. Birleştirme (A7), dosya sayısını ve okuma maliyetini sınırlar. |
| K7 | Oyun kabuğu (JS/CSS/manifest) **Media.temir'den** sunulur | Kod ile veri aynı patch'te güncellenir. Tauri bugün de böyle çalışıyor. Sürüm uyuşmazlığı oluşmaz. |
| K8 | Okumalar `read-only` sync handle'lı **birkaç worker'a** dağıtılır. WASM SIMD şimdilik yok. | A1: 4 worker ~290 MB/s, Tauri 146–174 MB/s. Tek worker'da şifre çözme %69 pay alıyor. Uygulandı. Açılış sahnesinde fark ölçülmedi (bkz. "K8 uygulaması"). |

## 5. `.temir` v1 formatı (okuyucu için özet)

Bütün sayılar little-endian. Ayrıntılar `temir.ts` dosyasının başlığında,
yazan taraf `apps/server/internal/agent/updates/temir.go` ve new-silkroad
`sropk.rs`.

| Bölüm | İçerik |
|---|---|
| Header (64 B) | `0..8` magic `TEMIR\x01\x00\x00`, `8..24` salt, `24..48` şifreli `{tocOffset, tocLength, entryCount}` (`TEMIR-HDR-01` nonce), `48..64` doğrulama etiketi |
| Üyeler | Her biri ana anahtar + kendi 12 baytlık nonce'u ile ChaCha20. Akış ofseti üyenin ilk byte'ında 0'dan başlar. |
| TOC | `TEMIR-TOC-01` nonce ile şifreli. Her kayıt: `u16` yol uzunluğu, yol (UTF-8), `u64` ofset, `u64` uzunluk, 12 B nonce, `u64` FNV-1a (düz metin) |
| Anahtar | Parola + salt → bayt karıştırma + 4096 tur ChaCha20 süngeri |
| Etiket | Salt, TOC konumu ve TOC şifreli metninin FNV-1a özeti üzerinden (`TEMIR-MAC-01`). Yanlış anahtar veya değişmiş TOC açılışta reddedilir. |
| Sayaç | 64 bit blok sayacı: yüksek kelimesi ilk nonce kelimesine XOR'lanır. Her byte O(1) ile çözülür. |

Dikkat edilecekler:

- **Üye içeriği kimlik doğrulamalı değil.** Etiket yalnızca header'ı ve
  TOC'u korur. Bozuk bir indirmeyi yakalamak için üye başına FNV-1a
  (`entry.checksum`) kontrolü kullanılır (A3).
- Sunucu `last_version/*.temir` dosyalarını **yerinde** değiştiriyor
  (`MergePatchIntoBaseArchives`). Kesintiden sonra devam eden bir indirme,
  arada değişmiş bir dosyanın parçalarını birleştirmemeli. Bu yüzden
  `If-Range` kullanılır, ve devam edilmeden önce header etiketi
  `patches.json`/`version.json` ile karşılaştırılır (A3).

## 6. Aşamalar

Her aşama bağımsız olarak birleştirilebilir. "Bitti" ölçütü karşılanmadan
bir sonraki aşamaya geçilmez.

### A0. Format okuyucu ve ortak fixture: **TAMAMLANDI**

- `apps/client-next/src/engine/foundation/assets/temir.ts`: `readTemirHeader`,
  `openTemirToc`, `decryptTemirRange`, `chachaXorAt`, `deriveTemirKey`,
  `fnv1a64`, `normalizeTemirPath`, `isTemirFormatError`. Durumsuz (bu bir
  `foundation/` kuralı, `verify-ownership` zorunlu kılıyor). I/O yapmaz,
  okunan byte'ları çağıran verir. Ana makinenin byte sırasına bağlı değil.
- `apps/server/internal/agent/updates/temir_fixture_test.go`: Go packer'ın
  ürettiği `testdata/client-reader.temir` dosyasını sabitler. Yeniden üretmek
  için: `SRO_WRITE_TEMIR_FIXTURE=1 go test -run TestTemirClientReaderFixture ./internal/agent/updates`
- `apps/client-next/tests/fixtures/temir/client-reader.temir`: kopya.
  `check:shared-fixtures` aynı kalmasını zorunlu kılar (bu fixture'da kaynak
  Go tarafı).
- `apps/client-next/tests/runtime/temir-reader.test.mjs`: RFC 8439 vektörü,
  FNV-1a bilinen değerleri, fixture'ın çözülmesi, blok ortasından okuma,
  yanlış anahtar, bozuk TOC.
- `.gitattributes` (kök ve `apps/server`): `*.temir binary`.

### A1. Prototip ölçümü: **TAMAMLANDI**

Amaç, hız varsayımını kanıtlamak ve K7'deki kilit riskini test etmek.

Araç: `apps/client-next/tools/perf/bench/temir-opfs-bench.mjs` (başlığında
kullanım var). Edge'i geçici bir profille açar, arşivi OPFS'e kopyalar,
gerçek anahtarla açar, üye özetlerini doğrular, aynı range planını tek
worker ve `read-only` modda N worker ile okur, kilit testini yapar.
`--tauri` verilirse aynı range'leri çalışan masaüstü kabuğundan da okur ve
byte'ların aynı olduğunu kontrol eder. Sonunda OPFS dosyasını siler.

```
node tools/perf/bench/temir-opfs-bench.mjs --archive ../../../new-silkroad/World.temir
node tools/perf/bench/temir-opfs-bench.mjs --archive ../../../new-silkroad/World.temir --tauri   # Silkroad.exe açıkken
```

Kabuk 15180 doluysa sonraki boş porta geçer (`lib.rs start_local_edge`).
Hangi porttan dinlediği `netstat -ano` ile bulunur ve `--tauri-origin
http://127.0.0.1:<port>` ile verilir.

- Yalnızca geliştirme ortamında çalışan bir sayfa ya da test: gerçek
  `World.temir` dosyasını OPFS'e kopyalar, dedicated worker'da açar. Bir
  sahnenin pack bloklarını (1 MB'lık range'ler) hem OPFS + `temir.ts` ile hem
  Tauri'nin `127.0.0.1:15180` adresinden okur ve süreleri karşılaştırır.
- Ölçülecekler: TOC açma süresi; blok başına okuma + çözme süresi
  (p50/p95); toplam sahne yükleme süresi; çözmenin toplam içindeki payı.
- Kilit testi: asset worker'da açık bir `SyncAccessHandle` dururken service
  worker'ın aynı dosyada `getFile()` çağırıp çağıramadığı. Chrome'da
  `mode: "read-only"` ve Safari ile Firefox davranışı ayrı ayrı test edilir.
- **Bitti:** Sonuçlar bu dokümanın 9. bölümüne yazılır. Çözme payı %30'un
  üzerindeyse `chachaXorAt` için WASM SIMD işi açılır.

### A2. Kurulum kapısı: **TAMAMLANDI**

- `src/engine/foundation/install/install-gate.ts`: `installMode`
  (`desktop-shell` / `installed` / `browser`), `installRoute` (`prompt` /
  `ios` / `mac-safari` / `unsupported` / `no-webgpu`), `showInstallGate`.
  Metinlerin tamamı `index.html`'deki statik işaretlemede (Türkçe). TS
  yalnızca `data-route` ve `data-state` değerlerini ayarlıyor.
  Stil: `install-gate.css`.
- Bayrak: `SRO_INSTALL_GATE=1` (`vite.config.mjs`,
  `import.meta.env.SRO_INSTALL_GATE`). Varsayılan **kapalı**: geliştirme
  ortamı, tarayıcı testleri ve upstream davranışı değişmez. Tauri
  (`OpenSRODesktop` UA) ve kurulu pencere kapıdan geçer.
- Manifest: `tools/pwa-manifest.mjs`. Kapı açıkken `/manifest.webmanifest`
  ve `/pwa-icon.png` dosyalarını geliştirme sunucusunda sunar, build'de
  çıktıya yazar ve `index.html`'e `<link rel="manifest">` ekler. İkon
  **depoda değil** (oyun görseli, NOTICE.md). `SRO_PWA_ICON` ile kare,
  en az 512 px'lik bir PNG verilmezse build hata verir. İsteğe bağlı:
  `SRO_PWA_NAME`, `SRO_PWA_SHORT_NAME`, `SRO_PWA_START_URL`.
- Chromium'un kurulum olayları `tools/types/install-prompt.d.ts` içinde
  tanımlı (TS DOM kütüphanesinde yoklar). Böylece execution map onları
  dış sınır olarak görüyor.
- Doğrulama (Edge 155, `SRO_INSTALL_GATE=1`, geliştirme sunucusu): sekmede
  `prompt` paneli görünüyor, oyun başlamıyor. Manifest hatası yok.
  Kalıcı profilde `Page.getInstallabilityErrors` **boş** dönüyor, service
  worker olmadan da kurulabiliyor. `beforeinstallprompt` geldi, düğme
  `ready` oldu. Bayrak kapalıyken kapı gizli, runtime çalışıyor.
- Eksik: iOS/macOS Safari ve Android'de gerçek cihaz kontrolü (A8).

Örnek build:

```
SRO_INSTALL_GATE=1 SRO_PWA_ICON=../../../new-silkroad/src-tauri/icons/icon.png pnpm --filter @sro/client-next build
```

### A3. OPFS deposu ve indirici: **çekirdek TAMAMLANDI, bağlantı (A3b) bekliyor**

- `src/engine/foundation/assets/temir-download.ts`:
  - `parsePatchManifest`: `patches.json`'daki 64 bit `checksum` değerleri
    2^53'ü aşıyor. `JSON.parse` bunları yuvarlar, bu yüzden metin olarak
    alınıp `BigInt`'e çevriliyor.
  - `baseArchiveReleases`: Media/Data/World kayıtlarını döndürür.
  - `downloadArchive` (async generator): bir sync handle'a doğrudan yazar.
    Her 8 MiB'da dosyayı flush eder ve ilerleme dosyasına `{offset, FNV
    durumu, validator}` yazar. Devam ederken kayıtlı noktaya kadar kırpar,
    `Range` + `If-Range` ister, FNV'yi kaldığı yerden sürdürür (diskteki
    byte'ları yeniden okumaz). Sunucu 200 dönerse ya da manifest başka bir
    build'i gösterirse baştan başlar. Sonda **boyut + bütün dosyanın FNV-1a'sı
    + header tag** doğrulanır, Tauri'nin `updater.rs`'teki kontrollerinin
    aynısı. Uyuşmazsa deneme silinir.
  - `archiveComplete`: her açılışta yapılan ucuz kontrol (kayıt `done`,
    boyut ve header tag eşleşiyor).
- `temir.ts`'e artımlı FNV eklendi (`fnv1a64Start` / `Update` / `Value`).
- `tools/types/opfs-sync-access.d.ts`: `FileSystemSyncAccessHandle`
  (TS'de yalnızca lib.webworker'da var) ve `mode` seçeneği.
- Testler (`tests/runtime/temir-download.test.mjs`, 10 test): Range/If-Range
  uygulayan sahte bir sunucu ve bellek içi handle ile; Go fixture'ı gerçek
  sürüm olarak kullanılıyor. Kopan bağlantı kontrol noktasından devam ediyor;
  sunucuda değişen dosya baştan başlıyor; yeni sürüm eskisini atıyor; uzun
  eski dosyadan çöp kalmıyor; yolda bozulan byte reddediliyor; yanlış header
  tag reddediliyor; biten indirme tekrar istenmiyor; onaylanmamış kuyruk
  kullanılmıyor. İki kritik satır bozularak testlerin yakaladığı doğrulandı.
- Bilinen sınır: FNV akış sırasında hesaplanıyor. Bir kontrol noktası
  yazıldıktan **sonra** diskte bozulan byte'lar indirme sırasında
  yakalanmaz (Tauri'de de aynı). Okuma sırasında üye başına FNV kontrolü
  (`entry.checksum`) gerekirse A4'te eklenebilir.

### A3b. İndiriciyi asset worker'a bağlamak: **TAMAMLANDI**

Uygulananlar:

- `src/engine/foundation/assets/archive-delivery.ts` (K5): `network` /
  `shell` / `opfs`. Dört ayrı `OpenSRODesktop` kontrolü (`assets.ts`,
  `loader.ts`, `packs.ts`, `blocks.ts`) buraya toplandı. Yerel arşiv okuması
  = Tauri veya kurulu PWA (Cache Storage yok, üye başına SHA-256 yok,
  masaüstü bütçeleri). `updatesBaseUrl`: `SRO_UPDATES_URL` (build) veya
  sayfanın kökenindeki `/updates/`.
- `src/engine/runtime/assets/worker/archives/archives.ts` (`loader.ts`'e
  ait): `prepare()` önce `patches.json`'ı okur, sonra eksik arşivleri
  `downloadArchive` ile indirir ve arşivleri açar. Boş alan önceden
  `storage.estimate()` ile kontrol edilmiyor: Brave parmak izi korumasında
  uydurma bir kota bildiriyor ve onlarca GB boş diski olan bir
  makinede ilk kurulumu "Not enough storage" ile reddetti (2026-10-26,
  ilk üretim denemesi). Yalnızca kökenin gerçekten reddettiği bir yazma
  (`QuotaExceededError`) sayılıyor. Hata mesajı ne kadar yer gerektiğini
  söylüyor, son kontrol noktasına kadarki byte'lar korunuyor ve yer
  açılınca indirme sürüyor. `read()`, `PackDownload` okumalarını karşılar.
  Tam dosya veya range okuyabilir. Yalnızca `.json.gz` varsa açar. Arşivde
  olmayan bir yol için 404 döner. Bütçe aşımında `ASSET_BYTE_LIMIT` hatası
  verir.
- **Yeniden yükleme kilidi:** Bir sayfa yeniden yüklenince yeni worker,
  eski worker handle'ları bırakmadan başlayabiliyor ve
  `createSyncAccessHandle` `NoModificationAllowedError` veriyor (Edge
  155'te uçtan uca testle bulundu). Açılış 250 ms aralıklarla en çok
  15 sn bekliyor. Bekleme fonksiyonu `loader.ts`'in
  `retryDownloadAfter`'ı, zamanlayıcı yeteneği orada.
- Sözleşme: `prepare-archives` isteği, `archives-ready` /
  `archives-failed` yanıtı. `assets.ts`, hazır olana kadar `available()`'ı
  0 döndürüyor. Bütün runtime sahipleri istekten önce `available()`'a
  bakıyor, bu yüzden worker'a hiçbir yükleme gönderilmiyor ve bekçi
  başlamıyor. Kurulum ilerlemesi mevcut `progress` mesajlarıyla yükleme
  ekranına gidiyor.
- Kayıtlar: `ownership.json`, `execution-contract.json`
  (`requestArchive`, `retryDownloadAfter`), `verify-capabilities.mjs`
  `ASYNC_OWNERS`.
- Testler: `tests/runtime/local-archives.test.mjs` (7 test). Bellek içi OPFS
  `tests/helpers/opfs-memory.mjs`'te, indirici testiyle ortak.

Uçtan uca doğrulama (2026-10-26, Edge 155 headless, `--app` penceresi
`display-mode: standalone` olarak görünüyor, kapı açık build, yerel bir
sunucu `dist` dosyalarını ve gerçek `.temir` dosyalarını Range/ETag ile
sunuyor, `patches.json` yerel dosyaların gerçek boyut, header tag ve FNV
değerleriyle):

| Kontrol | Sonuç |
|---|---|
| Kurulu pencere algılandı, kapı geçildi | ✅ |
| İlk indirme | Media (200) → Data (200) |
| 1,25 GiB'te sayfa yeniden yüklendi | Data **kaldığı yerden** devam etti: `Range: bytes=570425344-` (8 MiB'ın katı), `If-Range` ETag, **206** |
| Toplam sunulan | 3,93 GiB (arşivlerin toplamı 3,92 GiB, tekrar inen yalnızca son kontrol noktasından sonraki kısım) |
| Yeniden yüklemeden sonra tamamlanma | 30 sn (yerel sunucu, ~100 MB/s) |
| Ağdan pack isteği | **0** |
| Oyun | Açılış sahnesi (liman, gemiler) OPFS'ten çizildi, `Frontend: intro` |
| İkinci açılış | **0** arşiv isteği, 0 byte, doğrudan oyun |
| Yükleme ekranında kurulum ilerlemesi | "1331 MB read · 106 MB/s" |
| FNV çapraz kontrolü | Data.temir için TS'te hesaplanan FNV, üretimdeki `patches.json` değeriyle birebir aynı |
| Gerileme | Değişen asset modüllerine dokunan 29 test dosyası: 308/308 |

Açık kalanlar (A5'e):

- `index.html`'in statik yükleme görselleri (`/assets/images/...`) ve CSS
  imleci (`/assets/cursors/...`) web'den isteniyor. Kurulu PWA'da bunlar
  yalnızca Media.temir'de olduğu için **404** dönüyor (yükleme ekranında
  kırık logo). Kapı sayfası bu yüzden `/pwa-icon.png` kullanıyor. Kalıcı
  çözüm, service worker'ın bu yolları Media.temir'den sunması.
- Yükleme ekranı kurulum sırasında "Loading scene description" yazıyor.
  Kurulum için ayrı bir metin daha anlaşılır olur.

Önceki plan (referans için):

Üretim sunucusunda (2026-10-26) `/updates/` sunuluyor ama `/assets/packs/`
sunulmuyor (404) ve web istemcisi henüz yayında değil. Yani veri yalnızca
`.temir` olarak dağıtılıyor. Bu yüzden kurulu uygulama, Tauri'deki gibi
**ilk açılışta üç arşivi (~4,25 GB) indirip sonra oyunu başlatır**. "Ağdan
oyna, arka planda kur" seçeneği pack ağacının web'e yüklenmesini
gerektirdiği için şimdilik yok.

Kurulum hiçbir yükleme isteğinin **içinde** çalışmamalı: ana thread'in
bekçisinin 10 dakikalık mutlak sınırı var (`assets.ts`
`REQUEST_ABSOLUTE_MS`) ve yavaş bir bağlantıda ilk indirme bunu aşar. Plan:

- Mod: `import.meta.env.SRO_INSTALL_GATE === true` ve Tauri değil ⇒ kurulu
  PWA ⇒ OPFS'ten oku. Kapı açık bir build'de oyun başka yerde başlamadığı
  için ek bir mesaja gerek yok.
- K5: `DESKTOP_SHELL` kontrolleri (`assets.ts`, `loader.ts`, `packs.ts`,
  `blocks.ts`) tek bir yetenek fonksiyonuna toplanır: yerel arşiv okuması =
  Tauri veya kurulu PWA.
- `worker/archives/archives.ts` (`loader.ts`'e ait): `patches.json`,
  `storage.estimate()`, `downloadArchive` ile üç arşiv, sonra arşivleri
  açar ve `PackDownload` okumalarını karşılar (`.json` istenip yalnızca
  `.json.gz` varsa açarak, Tauri edge'in `Content-Encoding` davranışıyla
  aynı).
- Sözleşme: yeni bir `prepare-archives` isteği ve `archives-ready` /
  `archives-failed` yanıtı. `assets.ts`, hazır olana kadar yükleme
  isteklerini **worker'a göndermeden** bekletir (bekçi başlamaz).
  İlerleme mevcut `progress` mesajlarıyla yükleme ekranına gider.
- `execution-contract.json` ve capability kayıtları.
- **Bitti:** Kurulu PWA ilk açılışta arşivleri indirip oyunu OPFS'ten
  açıyor. Sekme kapatılıp açılınca indirme kaldığı yerden sürüyor.

**Not (A6 ile çözüldü):** Sunucu her güncellemede `last_version/*.temir`
dosyalarını yerinde değiştiriyor ve header tag değişiyor. A6'dan önce kurulu
PWA her güncellemede temel arşivi baştan indiriyordu. Artık eski temel arşivi
koruyup yalnızca patch'leri indiriyor (A6).

### A4. Oyuna bağlamak

- `PackDownload` (`packs/internal/pack-contract.ts`) için yerel arşiv
  kaynağı: `url` → `normalizeTemirPath` → üç arşivde arama → oku + çöz.
  `range` parametresi birebir karşılanır.
- `desktopShell` UA kontrolleri (`assets.ts`, `loader.ts`, `packs.ts`,
  `blocks.ts`) tek bir yetenek bayrağına bağlanır (K5). Tauri ve OPFS bu
  bayrağı açar.
- `src/engine/ownership.json` / `execution-contract.json` /
  `tools/verify-capabilities.mjs` kayıtları eklenir (`verify:quick`).
- **Bitti:** Kurulu PWA'da oyun ağdan hiçbir pack istemeden açılır (DevTools
  Network'te `/assets/packs/` isteği yok).

### A5. Uygulama kabuğu: **TAMAMLANDI (sadeleştirilmiş)**

Asıl plan, kabuğun (JS/CSS) bir service worker ile Media.temir'den
sunulmasıydı (K7). Uygulamada daha basit bir çözüm yeterli oldu. Sorun yalnızca
tarayıcının **doğrudan URL ile** yüklediği dosyalardı, ve kod tabanında
bunların tek bir listesi zaten var. K7 bu yüzden bekletiliyor (aşağıda).

- **Doğrudan yüklenen dosyalar:** `foundation/assets/native-assets.ts`
  `nativeAssetUrls()`, yani açılış ekranının 4 görseli (`index.html` +
  `loading.css`) ve 10 dünya imleci. Beta builder da bunları ayrıca
  yayımlıyor. Kapı açık bir build, `tools/pwa-manifest.mjs` ile bu 14 dosyayı
  (~230 KB) yayınlanmış asset ağacından (`CLIENT_PUBLIC_ROOT`) `dist`'e
  kopyalıyor. Biri eksikse build hata veriyor. Böylece görseller arşivler
  daha inmeden, ilk kurulum ekranında da görünüyor.
- **Dayanıklılık:** Başarılı her `prepare()`, kullandığı `patches.json`'ı
  OPFS'e kaydediyor (`temir/patches.json`). Sonraki bir açılışta `/updates/`
  erişilemezse (Agent yeniden başlıyor, bağlantı koptu) kayıtlı manifest
  kullanılıyor. Arşivler ona göre tamsa oyun açılıyor. İndirme gerekirse
  ya da ilk açılışsa orijinal hata (ör. `HTTP 503`) bildiriliyor.
- **Yükleme ekranı:** İlk indirme sırasında dosya etiketi "game files
  (Media)" / "game files (Patch012)" oluyor (`loading-detail.ts`).
- Testler: `local-archives.test.mjs` 14 test (çevrimdışı açılış ve çevrimdışı
  + eksik dosya eklendi; manifest'in kaydedilmesi ve indirme koruması
  bozularak testlerin yakaladığı doğrulandı), `install-gate.test.mjs` (native
  dosyaların yayımlanması ve eksik dosyada build hatası),
  `loading-detail.test.mjs`.
- Uçtan uca (Edge, `--app` penceresi, gerçek arşivler):
  - Kurulum ekranı logo, "Now Loading" ve gösterge çerçevesiyle eksiksiz.
  - Görsel ve imleç için **404 yok** (kalan 404'ler yalnızca test
    ortamında olmayan `/api/*`).
  - Ağdan pack isteği 0.
  - `patches.json` 503 dönerken yeniden açılış: **0 arşiv isteği**, oyun
    açılış sahnesine ulaştı.

Bekletilen (K7, yalnızca ihtiyaç olursa):

- Kabuğun (JS/CSS) Media.temir'den sunulması ve tam çevrimdışı açılış. Şu an
  kabuk sunucudan geliyor ve veri her açılışta `patches.json` ile eşitlendiği
  için sürüm uyuşmazlığı oluşmuyor. Oyun zaten sunucu gerektirdiği için tam
  çevrimdışı kabuğun faydası sınırlı. Gerekirse service worker, `getFile()`
  ile Media.temir'i okuyabilir (A1: Chromium'da kilit sorunu yok).

### A6. Patch'ler: overlay: **TAMAMLANDI**

- `temir-download.ts`: `PatchEntry` (`path`, `size`, `checksum`; 64 bit
  checksum'lar `BigInt` olarak), `isDifferentialPatch`, `livePatchEntries`
  (`sropk.rs` `live_patch_entries` + `bundle_family`: daha yeni bir patch'te
  aynı yol ya da aynı Vite paket ailesi (`assets/bootstrap-*.js`,
  `assets/index-*.css`) varsa eski girdi ölüdür, her yol en çok bir
  patch'te canlıdır). `completeAsRecorded`: dosya, kaydedildiği sürüm olarak
  tam mı (güncel sürüm olması gerekmez).
- `archives.ts` `prepare()`:
  1. Temel arşivler: ilk kurulum `last_version`'ı indirir (bütün patch'ler
     zaten içinde). Sunucu güncellendiyse ama elde **tam** bir eski temel
     arşiv varsa ("behind") ve sunucu patch yayımlıyorsa arşiv korunur
     (`updater.rs` de bağlı temel arşivi korur). Patch yoksa değişmiş temel
     arşiv baştan iner.
  2. Canlı girdileri temel arşivlerde aynı boyut ve checksum'la zaten
     bulunan patch indirilmez (`sropk.rs` `missing_patch_entries`) ve eski
     dosyası silinir. Diğerleri indirilir (tam dosya; boyut + FNV + header
     tag doğrulamasıyla) ve **yalnızca canlı girdileri** temel arşivlerin
     üstüne bindirilir.
  3. **Maliyet kuralı (Tauri'de yok):** Patch'ler tam dosya olarak iniyor
     ve üretimde çok büyük olabiliyorlar (2026-10-26 manifest'inde toplam
     2,3 GB; Patch010 tek başına 1 GB, Patch014 531 MB). Gereken patch'lerin
     toplamı, geride kalan temel arşivleri `last_version`'dan baştan
     indirmekten pahalıysa temel arşivler baştan iner ve hiçbir patch
     gerekmez. İlerleme kayıtları bu karar verilene kadar açık tutulur.
- `read()`: önce patch katmanı, sonra temel arşivler. Bütün bir `.json`
  isteğinde, Tauri edge'in sırasıyla önce `.json.gz` ikizi bakılır (bir
  patch yalnızca `.gz`'yi güncellese de görülür).
- Fixture: Go packer'dan ikinci bir arşiv, `client-reader-patch.temir`
  (değişmiş bir `manifest.json` ve yeni bir dosya), `check:shared-fixtures`
  ile istemciye kopyalanıyor.
- Testler: `local-archives.test.mjs` 12 test (güncellemede yalnızca patch
  iniyor ve okumalar onu görüyor; yeni bir dosya patch'ten, dokunulmayan
  dosya temel arşivden geliyor; `last_version` kurulumunda tutulan patch
  inmiyor; temel arşiv patch'i içerince patch dosyası siliniyor; patch yoksa
  temel arşiv baştan iniyor; maliyet kuralı). `temir-download.test.mjs`'e
  canlı girdi testi (aynı yol, paket ailesi, girdisiz patch, 2^64−1
  checksum). Üç kritik satır (eski temel arşivi koruma, katman, patch
  silme) ve maliyet kuralı bozularak testlerin yakaladığı doğrulandı.
- Bilinen sınırlar:
  - Manifest'ten tamamen kalkmış eski patch dosyaları silinmiyor (yalnızca
    manifest'te adı geçen ve artık gerekmeyenler siliniyor). OPFS dizini
    taranarak temizlenebilir.
  - Sunucu `baseline`'ı sıfırlayıp patch zincirini baştan başlatırsa, eski
    bir temel arşivin zincirden önceki değişiklikleri patch'lerde olmaz.
    Tauri'de de aynı varsayım geçerli. Böyle bir sıfırlama sunucu tarafında
    patch'ler kaldırılarak yapılmalı (patch yoksa temel arşiv baştan iner).
  - Gerçek üretim zinciriyle uçtan uca test yapılmadı. Bu, üretim
    sunucusundan yüzlerce MB ile 1 GB arası indirme gerektiriyor.

### K8 uygulaması: paralel okuyucu worker'lar: **TAMAMLANDI**

- `worker/archives/reader.ts` + `reader-entry.ts` (worker girişi) +
  `internal/reader-contract.ts` (yalnızca mesaj tipleri). Arşivler tamamlanınca
  `archives.ts` kendi read-write handle'larını kapatıyor ve
  `readerCount(navigator.hardwareConcurrency)` kadar okuyucu başlatıyor:
  en çok 4, page ve asset worker için 2 çekirdek kalacak şekilde, 2'nin
  altında hiç yok. Her okuyucu bütün arşiv dosyalarını `read-only` açıyor.
  Okuma isteği en az meşgul okuyucuya gidiyor. Okuyucu okuyup şifreyi
  çözüyor ve tamponu transfer ederek geri veriyor. Anahtarlar ve içindekiler
  tabloları asset worker'da kalıyor.
- **Geri dönüş:** Bir okuyucu dosyayı `read-only` açamazsa (o mod olmayan
  tarayıcı) okuyucular durduruluyor, asset worker handle'larını yeniden
  açıp tek başına okuyor (önceki davranış).
- Kayıtlar: `ownership.json` (modül, worker, sözleşme), `verify-capabilities`
  (`Worker`: `archives.ts`; async: `reader.ts`), `execution-contract.json`
  (`sendToArchives` capability'si, worker-port sınırı, resolver sınırları).
- Testler: `local-archives.test.mjs` 18 test. Gerçek `reader.ts` süreç içinde
  sahte bir port arkasında çalışıyor: aynı byte'lar ve okumalar üç okuyucuya
  dağılıyor; `read-only` olmayan tarayıcıda geri dönüş; `dispose` bekleyen
  okumaları reddediyor; okuyucu sayısı kuralı. Test yardımcısının kapalı
  handle'ı artık gerçeği gibi okumayı reddediyor. Böylece geri dönüş
  mutasyonu yakalanıyor (önceden yakalanmıyordu).
- **Oyun içi ölçüm (2026-10-26, Edge 155 headless, 14 çekirdek, gerçek
  arşivler, aynı OPFS kurulumu, iki build dönüşümlü, 4'er çalıştırma):**
  açılış sahnesine ("Frontend: intro") ulaşma süresi tek worker'la ortalama
  9401 ms, 4 okuyucuyla 9352 ms. **Anlamlı fark yok.** Açılış sahnesi
  okumayla sınırlı değil. Süreyi headless ortamdaki yazılımsal GPU ve
  çözme işleri belirliyor. Ham okuma hızı (A1: ~290'a karşı ~100 MB/s) ve
  asset worker'ın şifre çözmeden kurtulması, dünyada gezinirken akan veride
  ve yavaş CPU'larda fayda sağlayabilir. Bu henüz ölçülmedi. Özellik
  tutuluyor (zararı ölçülmedi), ama oyun içi kazancı kanıtlanmış değil.
- Ölçüm notu: CDP'nin `Emulation.setHardwareConcurrencyOverride` ayarı
  worker'lara uygulanmıyor. A/B karşılaştırması bu yüzden okuyucu sayısı
  0'a sabitlenmiş ikinci bir build ile yapıldı (commit edilmedi).

### A7. Patch'ler: yerinde birleştirme

- `merge_entries_into_archive` mantığı TS'e taşınır: yeni üyeler ve TOC
  dosyanın sonuna eklenir, `flush()` yapılır, ardından 64 baytlık header
  yazılır ve tekrar `flush()` yapılır. Birleştirme oyun başlamadan, hiçbir
  okuyucu dosyayı açık tutmuyorken yapılır.
- `compact_archive` eşikleri (64 MB ve %25 ölü bayt) korunur.
- **Bitti:** Birleştirme sırasında sekmeyi kapatmak arşivi bozmaz (eski ya da
  yeni hali kalır).

### A8. Platform matrisi ve yayın

#### Ön koşullar

1. **Geçerli HTTPS.** iOS ve Android, kendinden imzalı sertifikayla PWA
   kurmuyor. WebGPU de güvenli bağlam istiyor. Test, geçerli sertifikası
   olan bir alan adında yapılmalı (`sro.ftemir.com` ya da bir alt alan
   adı).
2. **Aynı köken.** Kapı build'i (`dist`) ile `/updates/` aynı kökenden
   sunulmalı. `SRO_UPDATES_URL` başka bir köken gösterirse, Agent'ın CORS
   ayarı o kökene izin vermeli.
3. **Range desteği (üretimde şu an YOK).** 2026-10-26'da
   `https://sro.ftemir.com/updates/...` adresine `Range: bytes=0-63`
   gönderildi. Cloudflare arkasından (`cf-cache-status: DYNAMIC`) `206`
   yerine **`200` ve dosyanın tamamı** döndü. Bu, `patches.json`'da,
   küçük patch dosyalarında ve `last_version`'da aynı. Agent'ın kendisi
   Range'i destekliyor: `TestArchiveRangeResume`
   (`internal/agent/updates/handler_test.go`) `206`'yı, doğru
   `Content-Range`'i ve eski bir `If-Range` için tam `200`'ü sabitliyor.
   `sro.ftemir.com`'un nginx yapılandırmasında (`location /updates/`)
   Range'i düşüren bir ayar yok, nginx bu başlığı varsayılan olarak
   iletiyor. En olası katman Cloudflare. Sonuç: indirici doğru davranıp
   baştan başlıyor, ama **kopan bir indirme kaldığı yerden devam
   edemiyor** (1,5 GB baştan iner). Kesinleştirmek için sunucuda:
   - `curl -s -D - -o /dev/null -r 0-9 http://127.0.0.1:8787/updates/Patch002.temir`
     → Agent (206 beklenir)
   - `curl -s -D - -o /dev/null -r 0-9 -k --resolve sro.ftemir.com:443:127.0.0.1 https://sro.ftemir.com/updates/Patch002.temir`
     → nginx, Cloudflare'siz (206 beklenir)

   İkisi de 206 ise sorun Cloudflare'de. Seçenekler: (a) güncelleme
   dosyalarını Cloudflare'den geçmeyen (DNS only) bir alt alan adından
   sunmak ve build'de `SRO_UPDATES_URL`'i ona yönlendirmek (Agent'ın CORS
   ayarı sayfanın kökenine izin vermeli); (b) Cloudflare'de `/updates/*`
   için kuralları gözden geçirmek. Bir doğrulayıcı da var: `Last-Modified`
   geliyor, `ETag` yok. İndirici `Last-Modified`'ı `If-Range` olarak
   kullanıyor.
4. Build:

   ```
   SRO_INSTALL_GATE=1 SRO_PWA_ICON=<kare ≥512 px PNG> pnpm --filter @sro/client-next build
   ```

   Çıktı `apps/client-next/temp/artifacts/dist`. Web kökü bu `dist`'i
   sunmalı. `/updates/`, `/api/` ve shard yolları Agent'a gitmeli.

#### `sro.ftemir.com`'a yayın (aaPanel nginx)

Mevcut yapılandırmada `/api/`, `/updates/` ve `/shards/` zaten Agent ile
GameWorld'e gidiyor. Geri kalan her şey site kökünden
(`/www/wwwroot/sro.ftemir.com`) sunuluyor. Yayın:

1. Kapı build'inin içeriğini (2026-10-26: `EYLUL2026/pwa-dist`, zip'i
   `pwa-dist.zip`, 23 dosya, 2,8 MB) site köküne kopyalamak. Panelin
   varsayılan `index.html`'i değişir, `404.html`/`502.html` kalabilir. Tauri
   istemcisi bundan etkilenmez: kendi edge'i üzerinden çalışıyor.
2. Önerilen nginx eklemeleri (sunucu bloğuna, `location /updates/`'in
   yanına):

   ```
   # Yeni bir yayından sonra tarayıcı eski index.html'e (eski paket adlarına)
   # takılı kalmasın. expires, add_header kullanmadığı için sunucu
   # düzeyindeki HSTS/Alt-Svc başlıkları korunur.
   location = /index.html {
       expires -1;
   }
   location = /manifest.webmanifest {
       types { }
       default_type application/manifest+json;
       expires -1;
   }
   ```

   Hash'li paketler (`/assets/*-<hash>.js`) mevcut `expires 12h` kuralıyla
   uyumlu, çünkü adları her build'de değişiyor.
3. İstemci ve sunucu protokolü: 2026-10-26'da sunucu `8bbf2b34`'ten
   derlenmiş ve protokol 10 bekliyor. Bu build de protokol 10 gönderiyor.
   Sunucu ilerlerse istemci de aynı commit'ten yeniden derlenmeli.

#### Cihaz başına kontrol listesi

Her cihazda sırayla:

1. Sekmede açılış: kurulum sayfası doğru rotayı gösteriyor mu (Chrome/Edge:
   düğme; iOS: Paylaş → Ana Ekrana Ekle; macOS Safari: Dosya → Dock'a Ekle;
   Firefox: desteklenmiyor; WebGPU yoksa: uyarı)?
2. Kurulum ve kurulu uygulamadan açılış: kapı geçiliyor mu, ilk indirme
   yükleme ekranında "game files (Media)" olarak görünüyor mu?
3. İndirme sırasında uygulamayı kapatıp açma: kaldığı yerden sürüyor mu
   (yukarıdaki Range düzeltmesinden sonra)?
4. İndirme bitince oyun açılıyor mu, ikinci açılışta hiç indirme olmuyor
   mu?
5. Okuyucu worker'lar: Safari ve Firefox'ta `read-only` handle var mı?
   Yoksa geri dönüşle tek worker'da okuma sorunsuz mu?
6. Bellek ve arka plan: (iOS/Android) uygulama arka plana alınıp geri
   gelince ne oluyor? Yeniden yükleniyorsa arşivler yeniden inmemeli.
7. `navigator.storage.persisted()` sonucu (DevTools ya da uzak hata
   ayıklama).

| Test | Chrome Win | Edge Win | Chrome Android | Safari iOS 26 (ana ekran) | Safari macOS (Dock) |
|---|---|---|---|---|---|
| Kurulum kapısı | ✅ (Edge 155 headless, A2) | | | | |
| `persisted()` | | | | | |
| İndirme + devam | ✅ yerel sunucuyla (A3b); üretimde Range yok | | | | |
| Okuma (tek / paralel okuyucu) | ✅ (A1, K8) | | | | |
| Bellek / arka plandan dönüş | | | | | |
| Patch overlay | ✅ birim testleri (A6) | | | | |

## 7. Riskler ve açık sorular

| Risk | Etki | Ne zaman netleşir |
|---|---|---|
| Asset worker'ın sync handle'ı ile service worker okumasının çakışması | Kabuk tasarımını (A5) belirler | **Chromium'da yok** (A1: handle `readwrite` ya da `read-only` açıkken `getFile()` çalışıyor). Safari ve Firefox: A8 |
| Tarayıcıda şifre çözmenin payı (tek worker'da %69) | Tek worker'da okuma ~105 MB/s ile sınırlı | A1 sonrası: paralel worker'lar veya WASM SIMD (9. bölüm) |
| iOS web içeriği bellek sınırı | Eski iPhone'larda sessiz yeniden yükleme | A8 (gerçek cihaz) |
| WebGPU parçalılığı (Android GPU'ları, iOS < 26) | Desteklenmeyen cihazlar oyunu açamaz | A2 kapısı + A8 |
| Kota / boş alan (~4,2 GB) | Mobilde indirme yarıda durur | Yazma reddedilince ne kadar yer gerektiği söyleniyor ve indirme sonra sürüyor. `estimate()` güvenilir değil (Brave) |
| Sunucunun `last_version`'ı yerinde değiştirmesi | Devam eden indirmede karışık parça | A3 (`If-Range` + etiket) |
| ChaCha20'nin JS'teki hızı | Yükleme süresinde çözmenin payı | A1 |

## 8. Lisans ve güvenlik notları

- Proje **AGPL-3.0-or-later**. Değiştirilmiş sürüm ağ üzerinden sunulduğu
  için, oyunculara çalışan sürümün tam kaynağı sunulmalı (13. madde). Bu
  aşamalarda yazılan kod da buna dahil. Fork'un GitHub'daki hali deploy
  edilenle aynı tutulmalı ve oyuna bir "Kaynak kod" linki konmalı.
- `.temir` parolası (`DefaultArchiveKey`) zaten açık kaynakta
  (`temir.go`, `sropk.rs`). PWA'da da tarayıcıdan okunabilir. **`.temir`
  şifrelemesi gizlilik sağlamaz**, yalnızca sıradan kurcalamaya karşı bir
  karartmadır. Hiçbir aşama bunun aksini varsaymamalı.
- AGPL yalnızca kodu kapsar. Oyun varlıkları üzerinde hak vermez
  ([NOTICE.md](../NOTICE.md)).

## 9. Ölçümler

| Tarih | Ortam | Ölçüm | Sonuç |
|---|---|---|---|
| 2026-10-26 | Node 24 (V8), Windows, tek iş parçacığı | `chachaXorAt`, 64 MB (ısınmış) | ~245 MB/s |
| 2026-10-26 | aynı | `chachaXorAt`, 4 KiB'lık çağrılar | ~230 MB/s |
| 2026-10-26 | aynı | `deriveTemirKey` (gerçek parola uzunluğu) | ~1,3 ms / arşiv |
| 2026-10-26 | A1, Edge 155, headless, World.temir (1,57 GB, 213 üye) | Arşivi OPFS'e kopyalama | 20,3 s (~74 MB/s) |
| 2026-10-26 | aynı | Header + TOC açma (anahtar türetme dahil) | 22,5 ms |
| 2026-10-26 | aynı | Gerçek anahtarla 8 üyenin FNV-1a özeti | 8/8 eşleşti |
| 2026-10-26 | aynı, 256 MB, 1 MB'lık range'ler, ikinci geçiş | Tek worker: `read` + çözme | ~112 MB/s; p50 8,3 ms, p95 11,5 ms; **sürenin %69'u çözme** (yalnızca okuma ~365 MB/s) |
| 2026-10-26 | aynı | `read-only` modda 4 worker | ~310 MB/s; p50 12,6 ms, p95 14,9 ms |
| 2026-10-26 | aynı | Kilit: handle açıkken sayfada `getFile()` | `readwrite`: çalışıyor; `read-only`: çalışıyor |
| 2026-10-26 | A1 `--tauri`, `Silkroad.exe` 2026-10-25 (edge 15181), aynı plan | Aynı range'ler Tauri edge'den (12 eşzamanlı) | 146–174 MB/s; istek başına p50 66–81 ms (12 istek kuyrukta) |
| 2026-10-26 | aynı çalıştırma | OPFS tek worker / `read-only` 4 worker | ~101 MB/s / ~290 MB/s |
| 2026-10-26 | aynı çalıştırma | Byte eşitliği (FNV-1a, 1 MB'lık tam range + bütün range'lerin uçları) | **aynı** |

A1'den çıkan sonuçlar:

- **K7 Chromium'da çözüldü.** Service worker, asset worker dosyayı tutarken
  kabuğu aynı arşivden okuyabilir.
- **Hız: 4 worker'lı OPFS, Tauri'den ~1,7–2 kat hızlı** (~290 MB/s'ye karşı
  146–174 MB/s, aynı range planı). Tek worker ise Tauri'nin ~%60'ı.
- **PWA, Tauri'nin Rust'ıyla aynı byte'ları çözüyor.** Gerçek anahtar ve
  gerçek arşivle doğrulandı.
- **Şifre çözme tek worker'da darboğaz** (%69, %30 eşiğinin üstünde).
  Tarayıcıdaki ChaCha20 worker içinde ~160 MB/s (Node'da ~245 MB/s). Kelime
  bazlı XOR denendi, kazanç olmadı: maliyet 20 turun kendisinde.
  **Karar (K8):** A4, okumaları `read-only` modda birkaç worker'a dağıtır.
  Bu Tauri'yi zaten geçiyor, bu yüzden WASM SIMD şimdilik gerekmiyor.
  `read-only` modun olmadığı tarayıcılarda (Safari ve Firefox, A8'de
  netleşecek) tek worker'a düşülür. Orada hız yetmezse WASM SIMD yeniden
  değerlendirilir.
- Ölçümler sıcak OS önbelleğinde yapıldı (dosya az önce yazılmıştı). Soğuk
  diskten okuma, yeniden başlatma sonrası A8'de ölçülecek.

## 10. Doğrulama

| Kapsam | Komut |
|---|---|
| Okuyucu testleri | `node --test apps/client-next/tests/runtime/temir-reader.test.mjs` |
| A1 ölçümü | `node tools/perf/bench/temir-opfs-bench.mjs --archive <yol> [--tauri]` (`apps/client-next` içinde) |
| Go fixture | `go test -run TestTemirClientReaderFixture ./internal/agent/updates` (`apps/server` içinde) |
| Fixture eşitliği | `node scripts/checks/check_shared_fixtures.mjs` |
| İstemci hızlı kapılar | `pnpm --filter @sro/client-next verify:quick` |
| Biçim / kodlama | `pnpm task check:format`, `pnpm task check:source-encoding` |
