FROM postgres:17-alpine
CMD ["sh", "-c", "psql \"$DATABASE_URL\" -v ON_ERROR_STOP=1 -tAc 'SELECT 1' && sleep infinity"]
