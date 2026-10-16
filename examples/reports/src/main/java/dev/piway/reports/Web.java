package dev.piway.reports;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;

import org.springframework.boot.SpringBootVersion;
import org.springframework.context.event.ContextClosedEvent;
import org.springframework.context.event.EventListener;
import org.springframework.core.io.FileSystemResource;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.servlet.mvc.method.annotation.SseEmitter;

/**
 * POST /api/reports {"kind": "csv" | "pdf"}, GET /api/reports[/{id}],
 * GET /files/{id}, GET /events (SSE), and the common contract.
 */
@RestController
public class Web {

    private final Reports reports;
    private final JdbcTemplate db;

    Web(Reports reports, JdbcTemplate db) {
        this.reports = reports;
        this.db = db;
    }

    @GetMapping("/healthz")
    String healthz() {
        db.queryForObject("SELECT 1", Integer.class);
        return "ok\n";
    }

    @GetMapping("/api/info")
    Map<String, Object> info() throws NoSuchAlgorithmException {
        String secret = System.getenv().getOrDefault("REPORTS_TOKEN", "");
        byte[] sum = MessageDigest.getInstance("SHA-256").digest(secret.getBytes(StandardCharsets.UTF_8));
        Runtime rt = Runtime.getRuntime();
        return Map.of(
            "app", "reports",
            "language", "Java",
            "runtime", "Java " + Runtime.version() + ", Spring Boot " + SpringBootVersion.getVersion(),
            "env", Map.of("GREETING", System.getenv().getOrDefault("GREETING", "")),
            "secret", Map.of("name", "REPORTS_TOKEN", "set", !secret.isEmpty(), "length", secret.length(),
                "sha256_prefix", HexFormat.of().formatHex(sum, 0, 4)),
            "jvm", Map.of("max_heap_mib", rt.maxMemory() >> 20, "cpus", rt.availableProcessors()));
    }

    @PostMapping("/api/reports")
    ResponseEntity<Map<String, Object>> create(@RequestBody Map<String, String> body) {
        String kind = body.getOrDefault("kind", "");
        if (!kind.equals("csv") && !kind.equals("pdf")) {
            return ResponseEntity.badRequest().body(Map.of("error", "kind is csv or pdf"));
        }
        return ResponseEntity.status(HttpStatus.ACCEPTED).body(reports.create(kind));
    }

    @GetMapping("/api/reports")
    List<Map<String, Object>> list() {
        return reports.list();
    }

    @GetMapping("/api/reports/{id}")
    ResponseEntity<Map<String, Object>> get(@PathVariable long id) {
        Map<String, Object> r = reports.get(id);
        return r == null ? ResponseEntity.notFound().build() : ResponseEntity.ok(r);
    }

    @GetMapping("/files/{id}")
    ResponseEntity<FileSystemResource> file(@PathVariable long id) {
        Map<String, Object> r = reports.get(id);
        if (r == null || r.get("file") == null) {
            return ResponseEntity.notFound().build();
        }
        String name = (String) r.get("file");
        return ResponseEntity.ok()
            .contentType("pdf".equals(r.get("kind")) ? MediaType.APPLICATION_PDF : MediaType.parseMediaType("text/csv"))
            .header(HttpHeaders.CONTENT_DISPOSITION, "attachment; filename=\"" + name + "\"")
            .body(new FileSystemResource(Reports.DIR.resolve(name)));
    }

    @GetMapping(path = "/events", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    SseEmitter events() {
        return reports.subscribe();
    }

    /** Before the graceful shutdown waits for requests: end the streams. */
    @EventListener(ContextClosedEvent.class)
    void closing() {
        reports.closeStreams();
    }
}
