package dev.piway.reports;

import java.net.URI;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;

import javax.sql.DataSource;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.jdbc.DataSourceBuilder;
import org.springframework.context.annotation.Bean;
import org.springframework.scheduling.concurrent.ThreadPoolTaskExecutor;

/**
 * reports: an example app for piway. Report requests are queued, generated
 * as CSV or PDF on a small thread pool from data in MariaDB, followed over
 * Server-Sent Events, and kept on the data volume.
 */
@SpringBootApplication
public class ReportsApplication {

    public static void main(String[] args) {
        SpringApplication.run(ReportsApplication.class, args);
    }

    /**
     * The database piway gives the app (run.databases: [mariadb]), from
     * DATABASE_URL: mysql://user:password@host:port/name.
     */
    @Bean
    DataSource dataSource() {
        String raw = System.getenv("DATABASE_URL");
        if (raw == null || raw.isEmpty()) {
            throw new IllegalStateException("DATABASE_URL is not set: is mariadb in run.databases?");
        }
        URI u = URI.create(raw);
        String[] userInfo = u.getRawUserInfo().split(":", 2);
        return DataSourceBuilder.create()
                .url("jdbc:mariadb://" + u.getHost() + ":" + (u.getPort() > 0 ? u.getPort() : 3306) + u.getPath())
                .username(URLDecoder.decode(userInfo[0], StandardCharsets.UTF_8))
                .password(URLDecoder.decode(userInfo[1], StandardCharsets.UTF_8))
                .build();
    }

    /**
     * Two report workers. On shutdown the reports in progress finish, up to
     * 20 seconds; queued ones stay queued in the database and are picked up
     * by the next version.
     */
    @Bean
    ThreadPoolTaskExecutor reportExecutor() {
        ThreadPoolTaskExecutor ex = new ThreadPoolTaskExecutor();
        ex.setCorePoolSize(2);
        ex.setMaxPoolSize(2);
        ex.setQueueCapacity(100);
        ex.setThreadNamePrefix("report-");
        ex.setWaitForTasksToCompleteOnShutdown(true);
        ex.setAwaitTerminationSeconds(20);
        return ex;
    }
}
