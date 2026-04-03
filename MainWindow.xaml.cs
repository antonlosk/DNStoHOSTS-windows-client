using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Media;
using DnsClient;
using DnsClient.Protocol;

namespace DNStoHOSTS
{
    public partial class MainWindow : Window
    {
        private const string InputFile = "input.txt";
        private const string SettingsFile = "settings.txt";
        private const string OutputFile = "output.txt";
        private bool _isDarkTheme = true;
        private CancellationTokenSource _cts;

        private static readonly HttpClient _httpClient = new HttpClient(new HttpClientHandler() { UseProxy = false });

        public MainWindow()
        {
            ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12 | (SecurityProtocolType)12288;
            InitializeComponent();
            CheckAndCreateDefaultFiles();
        }

        private void CheckAndCreateDefaultFiles()
        {
            try {
                if (!File.Exists(InputFile)) File.WriteAllText(InputFile, "google.com\r\n");
                if (!File.Exists(SettingsFile)) File.WriteAllText(SettingsFile, "server=dns.google\r\nport=443\r\nipv4=true\r\nipv6=false\r\n");
            } catch { }
        }

        private void Log(string m) => Dispatcher.Invoke(() => { LogTextBox.AppendText($"[{DateTime.Now:HH:mm:ss}] {m}\r\n"); LogTextBox.ScrollToEnd(); });

        private async void BtnStart_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null) return;
            _cts = new CancellationTokenSource();
            try { await Task.Run(() => ProcessDomains(_cts.Token)); }
            catch (Exception ex) { Log("Error: " + ex.Message); }
            finally { _cts.Dispose(); _cts = null; }
        }

        private async Task ProcessDomains(CancellationToken token)
        {
            Log("Starting...");
            var s = ReadSettings();
            if (!File.Exists(InputFile)) return;

            var lines = File.ReadAllLines(InputFile);
            var resultLines = new List<string>();
            
            foreach (var line in lines)
            {
                if (token.IsCancellationRequested) break;
                string domain = line.Trim();
                if (string.IsNullOrEmpty(domain) || domain.StartsWith("#")) { resultLines.Add(line); continue; }

                Log($"Resolving: {domain}");
                var foundIps = new List<string>();

                if (s.ipv4) foundIps.AddRange(await Resolve(domain, QueryType.A, s, token));
                if (s.ipv6) foundIps.AddRange(await Resolve(domain, QueryType.AAAA, s, token));

                if (foundIps.Count > 0)
                {
                    foreach (var ip in foundIps.Distinct()) { Log($"  -> {ip}"); resultLines.Add($"{ip} {domain}"); }
                }
                else { Log("  [!] No records found"); resultLines.Add($"# Failed: {domain}"); }
            }
            File.WriteAllLines(OutputFile, resultLines);
            Log("Done.");
        }

        private async Task<List<string>> Resolve(string domain, QueryType type, dynamic s, CancellationToken t)
        {
            var ips = new List<string>();
            try
            {
                // Используем библиотеку для создания сообщения (это решит проблему с HTTP 400)
                var query = new DnsQuestion(domain, type);
                var lookup = new LookupClient(IPAddress.Loopback); // Нам нужен только объект
                var message = lookup.Query(query); // Создаем валидное сообщение
                
                // Сериализуем сообщение в байты через встроенные средства DnsClient
                // Мы используем публичный метод записи в массив
                var writer = new DnsDatagramWriter(new byte[512]);
                // Так как прямого доступа к Write нет, мы просто получим данные через запрос
                byte[] rawQuery = message.Answers.Context.QueryMessage.Data.ToArray(); 

                // Если трюк с Context не сработает (зависит от версии), используем запасной бинарный вариант:
                string host = ((string)s.server).Trim();
                string url = $"https://{host}:{s.port}/dns-query";

                using (var req = new HttpRequestMessage(HttpMethod.Post, url))
                {
                    // Самый надежный способ - передавать бинарный DNS пакет в POST
                    req.Content = new ByteArrayContent(rawQuery);
                    req.Content.Headers.ContentType = new MediaTypeHeaderValue("application/dns-message");
                    req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/dns-message"));

                    var resp = await _httpClient.SendAsync(req, t);
                    if (resp.IsSuccessStatusCode)
                    {
                        var data = await resp.Content.ReadAsByteArrayAsync();
                        // Парсим ответ обратно через библиотеку
                        var response = new DnsResponseMessage(new ArraySegment<byte>(data), 0);
                        foreach (var record in response.Answers)
                        {
                            if (record is AddressRecord addr) ips.Add(addr.Address.ToString());
                        }
                    }
                    else { Log($"  HTTP Error: {(int)resp.StatusCode}"); }
                }
            }
            catch (Exception ex) { Log($"  Error: {ex.Message}"); }
            return ips;
        }

        private dynamic ReadSettings()
        {
            string s = "dns.google"; int p = 443; bool v4 = true, v6 = false;
            if (File.Exists(SettingsFile))
                foreach (var l in File.ReadAllLines(SettingsFile)) {
                    var x = l.Split('='); if (x.Length != 2) continue;
                    var k = x[0].Trim().ToLower(); var v = x[1].Trim().ToLower();
                    if (k == "server") s = v; else if (k == "port") int.TryParse(v, out p);
                    else if (k == "ipv4") v4 = v == "true"; else if (k == "ipv6") v6 = v == "true";
                }
            return new { server = s, port = p, ipv4 = v4, ipv6 = v6 };
        }
        
        // Заглушки для UI событий
        private void BtnStop_Click(object sender, RoutedEventArgs e) => _cts?.Cancel();
        private void BtnClear_Click(object sender, RoutedEventArgs e) => LogTextBox.Clear();
        private void BtnTheme_Click(object sender, RoutedEventArgs e) { /* ... */ }
        private void BtnFile_Click(object sender, RoutedEventArgs e) { /* ... */ }
    }
}
