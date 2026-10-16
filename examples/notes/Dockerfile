FROM php:8.4-apache

# pdo_mysql for MariaDB, and phpredis for sessions in Redis. Both compile
# on the Pi, once; later builds use the cached layer.
RUN docker-php-ext-install pdo_mysql \
 && pecl install redis \
 && docker-php-ext-enable redis \
 && rm -rf /tmp/pear

RUN { \
      echo 'upload_max_filesize = 6M'; \
      echo 'post_max_size = 8M'; \
      echo 'expose_php = Off'; \
      echo 'log_errors = On'; \
      echo 'error_log = /dev/stderr'; \
      echo 'memory_limit = 64M'; \
    } > /usr/local/etc/php/conf.d/notes.ini \
 && sed -i 's#/var/www/html#/var/www/public#' /etc/apache2/sites-available/000-default.conf \
 && echo 'FallbackResource /index.php' > /etc/apache2/conf-enabled/notes.conf \
 && echo 'ServerTokens Prod' >> /etc/apache2/conf-enabled/notes.conf

COPY public /var/www/public
COPY src /var/www/src
COPY entrypoint.sh /usr/local/bin/notes-entrypoint
# Apache's workers run as www-data; a new volume mounted at /data starts as
# a copy of this directory, owner included, so they can write to it.
RUN mkdir -p /data/attachments && chown -R www-data /data

# The image stops Apache with SIGWINCH (graceful: requests in flight finish).
CMD ["notes-entrypoint"]
