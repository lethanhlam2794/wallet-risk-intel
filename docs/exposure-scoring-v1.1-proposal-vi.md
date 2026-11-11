# Đề xuất: Chấm điểm Exposure theo độ đa dạng nguồn rủi ro — Chính sách v1.1

> Trạng thái: ĐỀ XUẤT — chờ review theo §15 (gate thay đổi chính sách chấm điểm)
> Tác giả: Devin | Ngày: 2026-11-10
> Thay thế: bản nháp amount-weighted theo tỷ lệ share (cùng file, xem git history)
> Liên quan: `internal/scoring/policy.go`, `internal/scoring/engine.go`, `internal/scoring/model.go`

## 1. Vấn đề

Chính sách v1 chấm exposure gián tiếp **phẳng theo hop** (hop-1 → 7, hop-2 → 4,
trần 20). Hai khuyếm điểm:

1. Ví nhận **95% tiền của ví HACK** được chấm y hệt **7 điểm** như ví nhận
   **$5 dust**.
2. Bản sửa theo tỷ lệ share trước đó **bị né bằng cách chia nhỏ**: nguồn rủi
   ro chia tiền ra N ví làm tỷ lệ từng ví rơi xuống dưới ngưỡng chấm
   ($50M → 1,000 ví ⇒ mỗi ví ~0.1% ⇒ 0 điểm). Fan-out có chủ đích đánh bại
   được cơ chế phát hiện.

Yêu cầu của operator: chấm exposure theo cách điều tra viên kết tội hub rửa
tiền thực tế — **độ đa dạng của các nguồn bẩn độc lập**. Đụng 1 nguồn rủi ro
có thể là trùng hợp; đụng N nguồn labeled độc lập thì không thể là trùng hợp.

## 2. Ràng buộc (bất biến)

- **Exposure gián tiếp không bao giờ ngang attribution trực tiếp.** Exposure
  vẫn nằm trong block có trần riêng. Thay đổi được operator duyệt trong đề
  xuất này: trần exposure 20 → **70** — hai nguồn độc lập gần bão hòa; block
  30 điểm còn lại (`other`) dành cho tín hiệu nền tảng ngoài (provider khác
  có flag/cấm ví hay không và lý do gì). Trần exposure-only (70) chạm đáy dải
  direct; cộng thêm corroboration ngoài có thể chạm 100 — chấp nhận được vì
  đòi hỏi ĐỒNG THỜI evidence onchain nhiều nguồn sâu VÀ flag độc lập từ nền
  tảng, còn finding kind vẫn giữ phân biệt direct/indirect rõ ràng.
- Điểm **tái lập được theo policy version** — rows v1 không đổi; v1.1 là
  version MỚI được chọn theo request/config.
- `reported` vẫn là `reported`; `unknown` không bao giờ đọc là sạch.
- Không provider trả phí mới; không cross-chain; hop vẫn ≤2.
- Gate §15: tài liệu này CHÍNH LÀ artifact review — chưa implement trước
  khi duyệt.

## 3. Thiết kế — chấm theo số nguồn rủi ro khác nhau

### 3.1 Định nghĩa nguồn rủi ro

Ví **R** là *nguồn rủi ro* của ứng viên **X** khi **đồng thời**:

1. **Inbound có ý nghĩa**: có ít nhất 1 transfer đã xác nhận `R → X` giá trị
   ≥ `MEANINGFUL_TX_FLOOR_USD` (mặc định **$100**, hằng số chính sách), quy
   đổi qua bảng giá tĩnh ghim theo policy (§3.3). Transfer dưới sàn chỉ tạo
   finding evidence (0 điểm) — đây là lọc dust.
2. **R bị nhiễm bẩn**, theo một trong hai cách:
   - **nhiễm trực tiếp**: R mang label có chấm điểm (SANCTIONED, HACK,
     RANSOMWARE, PHISHING, TERRORISM, SCAM, DRAINER, MONEY_LAUNDERING,
     DARKNET, EXTREMISM, FROZEN) hoặc đang nằm trong danh sách sanctions
     còn hiệu lực; hoặc
   - **nhiễm theo chuỗi**: R là `proceeds_receiver` (§3.5) — taint của R đến
     từ inbound có ý nghĩa của một nguồn *nhiễm trực tiếp*. Độ sâu đúng 1:
     taint chuỗi không bao giờ đến từ một receiver khác.

Các phân loại dịch vụ (EXCHANGE, GAMBLING, MIXER) **không** phải nguồn nhiễm —
nhận tiền từ Binance không phải exposure.

### 3.2 Chấm theo từng nguồn khác nhau + thưởng tần suất

```
per_source = 30 + 7 × (n − 1)
             n = số transfer inbound đã xác nhận, có ý nghĩa, từ nguồn đó
                 (chỉ transfer ≥ sàn mới tăng n)

exposure = Σ per_source theo từng địa chỉ nguồn rủi ro RIÊNG BIỆT
             bằng chứng onchain tự tính → công thức trên (verified)
             chỉ có provider báo        → phẳng +21 (không có dữ liệu tx
               để đếm tần suất; reported vẫn là reported)
           dedupe theo địa chỉ nguồn xuyên các basis (basis cao hơn thắng)
           tín hiệu exposure chung của provider không định danh được
             counterparty giữ trọng số v1 phẳng (7/4), gộp vào cùng block

score = min(100, direct_max + min(70, exposure) + min(30, other))
```

- **Đa dạng nguồn là tín hiệu chính**: mỗi nguồn riêng biệt khởi đầu +30 —
  hai nguồn độc lập đạt 60; nguồn thứ ba, hoạt động lặp, hoặc kênh đơn sâu
  hoàn tất cap (70).
- **Tần suất là tín hiệu phụ**: transfer có ý nghĩa lặp lại từ cùng nguồn
  cộng +7 mỗi lần — một kênh thanh toán liên tục tới ví labeled là bằng
  chứng mạnh hơn một lần đụng. Một nguồn siêu hoạt động cũng tự chạm cap
  (7 tx ⇒ 72 ⇒ 70), nên "nhiều nguồn độc lập" HOẶC "một quan hệ sâu" đều
  bão hòa.
- Nhiều asset từ cùng R vẫn một nguồn; mỗi tx có ý nghĩa đếm một lần bất
  kể asset.
- Flow outbound `X → risky` KHÔNG chấm — phát finding informational
  `possible_victim_contact` (0 điểm): trả tiền cho scammer là evidence đối
  tác có thể muốn xem, nhưng nạn nhân không bị gắn cờ rủi ro.

### 3.3 Sàn ý nghĩa — bảng giá tĩnh ghim theo policy

Sàn định danh theo USD nhưng không cần oracle trực tiếp:

- Policy v1.1 ghim một **bảng giá tĩnh per-asset** (chỉ majors: ETH, BNB,
  USDT, USDC, DAI + allowlist mở rộng qua config), lưu như hằng số version
  hoá của chính sách — xác định, audit được, tái lập được.
- **Asset không có giá**: không chứng minh được materiality → finding
  evidence-only với `reason: "unpriced_asset"` (0 điểm). Không bịa giá trị;
  evidence vẫn được ghi cho đối tác.
- Trôi giá bị chặn vì bảng giá ghim **theo policy version** — cập nhật giá
  nghĩa là bump version mới, không phải re-score ngầm.

### 3.4 Ứng viên mang phân loại dịch vụ

Nếu chính ứng viên mang phân loại dịch vụ (EXCHANGE, GAMBLING, MIXER), các
finding exposure chỉ phát **informational (0 điểm)**. Đa dạng tiếp xúc là mô
hình kinh doanh của các thực thể đó — rủi ro của chúng phải đến từ bằng chứng
trực tiếp, không phải sự lân cận. (Danh sách phân loại là hằng số chính sách;
reviewer có thể thu hẹp.)

### 3.5 Cờ `proceeds_receiver` + lan truyền độ sâu 1

- Một ví bị gắn cờ `proceeds_receiver` khi có ≥1 inbound đã xác nhận, có ý
  nghĩa, từ nguồn **nhiễm trực tiếp**.
- Phát như finding informational trên chính screening của ví đó (asset,
  bằng chứng amount, tx hash, địa chỉ nguồn).
- Khi R xuất hiện như nguồn của ứng viên X, trạng thái receiver của R được
  **tính tại thời điểm screening** từ transactions đã index + nhãn của R —
  xác định, không phụ thuộc R đã từng được screen chưa. Cờ denormalized có
  thể lưu để tối ưu hiệu năng nhưng không bao giờ bắt buộc cho tính đúng đắn.
- Giới hạn độ sâu: taint chuỗi chỉ đến từ nguồn nhiễm trực tiếp —
  `A(labeled) → B(receiver) → C` chấm C qua B; không lan tiếp tới D. Tổng
  tầm với vẫn nằm trong bound 2-hop.

### 3.6 Versioning & lựa chọn

- `PolicyV1_1 = "v1.1.0"` đăng ký cạnh `PolicyV1`; path v1 inert khi chọn
  v1.1 và ngược lại.
- Env `SCORING_POLICY` (mặc định `v1.0.0`); tuỳ chọn `policy_version` per
  request để so sánh A/B. Version lạ → lỗi config lúc startup (fail-closed).

## 4. Edge case (bắt buộc xử lý + test)

- **Dust**: inbound < sàn → finding evidence, 0 điểm, `reason:
  "below_materiality_floor"` — và KHÔNG tăng bộ đếm tần suất (dust không
  tạo được quan hệ lặp giả).
- **Self-transfer** loại khỏi cả phát hiện nguồn lẫn đếm tần suất.
- Transfer **unconfirmed / bị reorg** loại khỏi cả hai.
- **Index coverage partial** ở nguồn hoặc ứng viên → finding mang
  `coverage: "partial"` (bất biến trung thực — dữ liệu thiếu không được
  trình bày như đủ).
- **Cả hai chiều** với cùng R: chỉ inbound được tính.
- **Cùng nguồn xuyên basis** (onchain + provider-reported): tính một lần ở
  basis cao hơn.
- **Nhiều asset** từ cùng nguồn: vẫn một nguồn (+30).
- **Ứng viên phân loại dịch vụ**: toàn bộ exposure informational.
- **Không có inbound nào có ý nghĩa**: không nguồn → không điểm exposure;
  ghi rõ lý do informational, không im lặng bỏ qua.
- **Độ sâu chuỗi**: receiver-của-receiver không lan truyền.

## 5. Non-goal rõ ràng

- Không chấm hop ≥3; không lan truyền đệ quy quá độ sâu 1.
- Không exposure cross-chain.
- Không đổi block direct label/sanctions/provider-score.
- Không re-score hồi tố — điểm mới chỉ áp cho screening mới.
- Không oracle giá trực tiếp (chỉ bảng tĩnh ghim theo policy).
- Không chấm theo cluster/pattern đồ thị (phát hiện signature fan-out là
  việc tương lai, v1.2+).

## 6. Rủi ro

- **Gaming bằng nhiều địa chỉ nguồn**: attacker có thể cố làm nạn nhân tăng
  điểm bằng cách gửi từ nhiều ví labeled — nhưng label là tài nguyên hiếm
  (attacker không mint được label) và sàn $ làm mỗi transfer đóng góp đều
  tốn kém. Kết quả: attacker đốt asset đã bị gắn nhãn để cờ người lạ; chấp
  nhận được.
- **Griefing tần suất**: 7×$100 ≈ $700 dựng kênh thanh toán lặp giả và đẩy
  nạn nhân lên cap 70. Giảm thiểu: finding mang số tx per nguồn nên "một
  kênh sâu" vs "nhiều nguồn độc lập" phân biệt được trong evidence dù điểm
  bằng nhau; sàn giữ cho mỗi tín hiệu giả đều tốn tiền.
- **Exposure cộng với provider-score**: trần exposure-only là 70; với block
  provider-score tối đa (+30) một ví có thể đạt 100 = trần sanctions.
  Operator đã duyệt: để chạm mức đó cần ĐỒNG THỜI evidence onchain nhiều
  nguồn VÀ flag độc lập từ nền tảng — evidence hội tụ, không chỉ sự lân
  cận. Finding kind vẫn giữ sự phân biệt direct/indirect rõ ràng trong
  mọi response.
- **Bảng giá tĩnh bị trôi**: ETH $3K vs $10K làm giá trị thật của sàn lệch
  ~3× — bị chặn bởi bảng ghim theo version; ghi nhận là giới hạn.
- **Chi phí tính**: query phát hiện nguồn per-ứng viên trên bảng
  `transactions`, bị chặn bởi maxHops=2 và index sẵn có; check receiver độ
  sâu 1 thêm một lookup có bound per nguồn ứng viên. Validate bằng EXPLAIN
  trên bảng ~1M rows.

## 7. Lộ trình triển khai

1. Review §15 tài liệu này.
2. Implement sau `SCORING_POLICY` mặc định `v1.0.0` — không thay đổi hành
   vi cho tới khi lật.
3. Chạy shadow v1.1 vs v1 trên mẫu ví mang risky-label; diff findings;
   chỉnh sàn/tier nếu lệch.
4. Lật mặc định; ghi tài liệu; đối tác chỉ thấy `v1.1.0` trên screening mới.
