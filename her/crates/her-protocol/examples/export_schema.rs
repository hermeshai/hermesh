fn main() {
    use std::io::Write;
    let mut out = std::io::stdout().lock();
    serde_json::to_writer_pretty(&mut out, &her_protocol::schema::document()).unwrap();
    writeln!(out).unwrap();
}
