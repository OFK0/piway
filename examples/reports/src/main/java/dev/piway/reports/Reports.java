package dev.piway.reports;

import java.io.BufferedWriter;
import java.io.IOException;
import java.io.OutputStream;
import java.math.BigDecimal;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.sql.Timestamp;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.concurrent.CopyOnWriteArrayList;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.event.EventListener;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.scheduling.concurrent.ThreadPoolTaskExecutor;
import org.springframework.stereotype.Service;
import org.springframework.web.servlet.mvc.method.annotation.SseEmitter;

import com.lowagie.text.Document;
import com.lowagie.text.Element;
import com.lowagie.text.Font;
import com.lowagie.text.FontFactory;
import com.lowagie.text.PageSize;
import com.lowagie.text.Paragraph;
import com.lowagie.text.Phrase;
import com.lowagie.text.pdf.PdfPCell;
import com.lowagie.text.pdf.PdfPTable;
import com.lowagie.text.pdf.PdfWriter;

/** The reports: the schema, generation on the pool, and progress events. */
@Service
public class Reports {

    private static final Logger log = LoggerFactory.getLogger(Reports.class);
    static final Path DIR = Path.of(System.getenv().getOrDefault("DATA_DIR", "/data"), "reports");
    private static final int SALES = Integer.parseInt(System.getenv().getOrDefault("SAMPLE_ROWS", "100000"));
    private static final int PAGE = 5000;
    private static final int KEEP = 50;

    private final JdbcTemplate db;
    private final ThreadPoolTaskExecutor pool;
    private final List<SseEmitter> listeners = new CopyOnWriteArrayList<>();

    Reports(JdbcTemplate db, ThreadPoolTaskExecutor reportExecutor) {
        this.db = db;
        this.pool = reportExecutor;
    }

    /**
     * Makes the tables (additive only: safe while the previous version
     * still runs during a deploy), fills the sample data once, and picks up
     * the reports a previous version left.
     */
    @EventListener(ApplicationReadyEvent.class)
    void start() throws IOException {
        Files.createDirectories(DIR);
        db.execute("""
            CREATE TABLE IF NOT EXISTS sales (
              id INT AUTO_INCREMENT PRIMARY KEY, sold_on DATE NOT NULL, region VARCHAR(20) NOT NULL,
              product VARCHAR(40) NOT NULL, quantity INT NOT NULL, amount DECIMAL(10,2) NOT NULL,
              INDEX (sold_on))""");
        db.execute("""
            CREATE TABLE IF NOT EXISTS reports (
              id BIGINT AUTO_INCREMENT PRIMARY KEY, kind VARCHAR(8) NOT NULL, state VARCHAR(10) NOT NULL,
              progress INT NOT NULL DEFAULT 0, row_count INT NOT NULL DEFAULT 0, file VARCHAR(100), error VARCHAR(300),
              created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), finished_at TIMESTAMP(3) NULL)""");
        Integer have = db.queryForObject("SELECT COUNT(*) FROM sales", Integer.class);
        if (have != null && have == 0) {
            seed();
        }
        // A version killed mid-report left it running; run it again.
        db.update("UPDATE reports SET state = 'queued', progress = 0 WHERE state = 'running' AND created_at < NOW() - INTERVAL 1 MINUTE");
        for (Long id : db.queryForList("SELECT id FROM reports WHERE state = 'queued' ORDER BY id", Long.class)) {
            submit(id);
        }
        log.info("ready: {} sample sales, reports in {}", db.queryForObject("SELECT COUNT(*) FROM sales", Integer.class), DIR);
    }

    private void seed() {
        String[] regions = {"North", "South", "East", "West", "Central"};
        String[] products = {"Widget", "Gadget", "Sprocket", "Gizmo", "Doohickey", "Thingamajig"};
        Random r = new Random(42);
        LocalDate start = LocalDate.now().minusYears(1);
        long t0 = System.nanoTime();
        List<Object[]> batch = new ArrayList<>(PAGE);
        for (int i = 0; i < SALES; i++) {
            int q = 1 + r.nextInt(20);
            batch.add(new Object[] {start.plusDays(r.nextInt(366)), regions[r.nextInt(regions.length)],
                products[r.nextInt(products.length)], q, BigDecimal.valueOf(q * (5 + r.nextInt(9500)) / 100.0)});
            if (batch.size() == PAGE || i == SALES - 1) {
                db.batchUpdate("INSERT INTO sales (sold_on, region, product, quantity, amount) VALUES (?, ?, ?, ?, ?)", batch);
                batch.clear();
            }
        }
        log.info("seeded {} sales in {} ms", SALES, (System.nanoTime() - t0) / 1_000_000);
    }

    public Map<String, Object> create(String kind) {
        db.update("INSERT INTO reports (kind, state) VALUES (?, 'queued')", kind);
        Long id = db.queryForObject("SELECT LAST_INSERT_ID()", Long.class);
        submit(id);
        Map<String, Object> r = get(id);
        publish(r);
        return r;
    }

    public List<Map<String, Object>> list() {
        return db.queryForList("SELECT * FROM reports ORDER BY id DESC LIMIT 50");
    }

    public Map<String, Object> get(long id) {
        List<Map<String, Object>> rows = db.queryForList("SELECT * FROM reports WHERE id = ?", id);
        return rows.isEmpty() ? null : rows.get(0);
    }

    private void submit(long id) {
        pool.execute(() -> run(id));
    }

    /** Claims a queued report (only one version runs it) and makes it. */
    private void run(long id) {
        if (db.update("UPDATE reports SET state = 'running' WHERE id = ? AND state = 'queued'", id) != 1) {
            return;
        }
        Map<String, Object> r = get(id);
        String kind = (String) r.get("kind");
        long t0 = System.nanoTime();
        Path tmp = DIR.resolve("report-" + id + ".tmp");
        try {
            int rows = "csv".equals(kind) ? csv(id, tmp) : pdf(id, tmp);
            String file = "report-" + id + "." + kind;
            Files.move(tmp, DIR.resolve(file), StandardCopyOption.REPLACE_EXISTING);
            db.update("UPDATE reports SET state = 'done', progress = 100, row_count = ?, file = ?, finished_at = ? WHERE id = ?",
                rows, file, new Timestamp(System.currentTimeMillis()), id);
            log.info("report {} ({}) done: {} rows in {} ms", id, kind, rows, (System.nanoTime() - t0) / 1_000_000);
        } catch (Exception e) {
            db.update("UPDATE reports SET state = 'failed', error = ? WHERE id = ?", String.valueOf(e.getMessage()), id);
            log.warn("report {} failed: {}", id, e.toString());
        }
        publish(get(id));
        prune();
    }

    private void progress(long id, int percent) {
        db.update("UPDATE reports SET progress = ? WHERE id = ?", percent, id);
        publish(get(id));
    }

    /** Every sale, a page at a time, so memory stays flat however many. */
    private int csv(long id, Path out) throws IOException {
        Integer total = db.queryForObject("SELECT COUNT(*) FROM sales", Integer.class);
        int written = 0;
        int lastId = 0;
        try (BufferedWriter w = Files.newBufferedWriter(out, StandardCharsets.UTF_8)) {
            w.write("id,sold_on,region,product,quantity,amount\n");
            while (true) {
                List<Map<String, Object>> page = db.queryForList(
                    "SELECT * FROM sales WHERE id > ? ORDER BY id LIMIT " + PAGE, lastId);
                if (page.isEmpty()) {
                    break;
                }
                for (Map<String, Object> s : page) {
                    w.write(s.get("id") + "," + s.get("sold_on") + "," + s.get("region") + "," + s.get("product")
                        + "," + s.get("quantity") + "," + s.get("amount") + "\n");
                }
                written += page.size();
                lastId = ((Number) page.get(page.size() - 1).get("id")).intValue();
                progress(id, (int) (95L * written / Math.max(1, total)));
            }
        }
        return written;
    }

    /** Monthly totals per region, as a table. */
    private int pdf(long id, Path out) throws IOException {
        progress(id, 10);
        List<Map<String, Object>> totals = db.queryForList("""
            SELECT DATE_FORMAT(sold_on, '%Y-%m') AS month, region, SUM(quantity) AS quantity, SUM(amount) AS amount
            FROM sales GROUP BY month, region ORDER BY month, region""");
        progress(id, 60);
        try (OutputStream os = Files.newOutputStream(out)) {
            Document doc = new Document(PageSize.A4, 36, 36, 36, 36);
            PdfWriter.getInstance(doc, os);
            doc.open();
            doc.add(new Paragraph("Sales by month and region", FontFactory.getFont(FontFactory.HELVETICA_BOLD, 16)));
            doc.add(new Paragraph("Generated by the reports example on piway, report " + id,
                FontFactory.getFont(FontFactory.HELVETICA, 9)));
            doc.add(new Paragraph(" "));
            PdfPTable t = new PdfPTable(new float[] {2, 2, 1.5f, 2});
            t.setWidthPercentage(100);
            Font head = FontFactory.getFont(FontFactory.HELVETICA_BOLD, 9);
            Font body = FontFactory.getFont(FontFactory.HELVETICA, 9);
            for (String h : new String[] {"Month", "Region", "Quantity", "Amount"}) {
                t.addCell(new PdfPCell(new Phrase(h, head)));
            }
            for (Map<String, Object> row : totals) {
                t.addCell(new Phrase(String.valueOf(row.get("month")), body));
                t.addCell(new Phrase(String.valueOf(row.get("region")), body));
                PdfPCell q = new PdfPCell(new Phrase(String.valueOf(row.get("quantity")), body));
                q.setHorizontalAlignment(Element.ALIGN_RIGHT);
                t.addCell(q);
                PdfPCell a = new PdfPCell(new Phrase(String.format("%,.2f", (BigDecimal) row.get("amount")), body));
                a.setHorizontalAlignment(Element.ALIGN_RIGHT);
                t.addCell(a);
            }
            doc.add(t);
            doc.close();
        }
        return totals.size();
    }

    /** Keeps the newest KEEP reports' files. */
    private void prune() {
        for (Map<String, Object> old : db.queryForList(
                "SELECT id, file FROM reports WHERE file IS NOT NULL ORDER BY id DESC LIMIT 1000 OFFSET " + KEEP)) {
            try {
                Files.deleteIfExists(DIR.resolve((String) old.get("file")));
            } catch (IOException e) {
                log.warn("prune: {}", e.toString());
            }
            db.update("UPDATE reports SET file = NULL WHERE id = ?", old.get("id"));
        }
    }

    public SseEmitter subscribe() {
        SseEmitter e = new SseEmitter(30 * 60 * 1000L); // the page reconnects after
        listeners.add(e);
        e.onCompletion(() -> listeners.remove(e));
        e.onTimeout(() -> listeners.remove(e));
        e.onError(t -> listeners.remove(e));
        // Spring sends the response headers with the first event; without
        // one the client waits (and may time out) until a report changes.
        try {
            e.send(SseEmitter.event().comment("connected"));
        } catch (IOException gone) {
            listeners.remove(e);
        }
        return e;
    }

    private void publish(Map<String, Object> report) {
        for (SseEmitter e : listeners) {
            try {
                e.send(report);
            } catch (Exception gone) {
                listeners.remove(e);
            }
        }
    }

    /** Ends every stream, so a shutdown does not wait for them. */
    public void closeStreams() {
        listeners.forEach(SseEmitter::complete);
        listeners.clear();
    }
}
