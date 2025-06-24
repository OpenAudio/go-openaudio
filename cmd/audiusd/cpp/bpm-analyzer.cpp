#include <aubio/aubio.h>
#include <iostream>
#include <string>
#include <vector>
#include <map>
#include <iomanip>

// Normalize BPM to 50-160 range
int normalize_bpm(int bpm) {
    if (bpm >= 50 && bpm <= 160) return bpm;

    if (bpm > 160) {
        if (bpm / 2 >= 50 && bpm / 2 <= 160) return bpm / 2;
        if (bpm / 3 >= 50 && bpm / 3 <= 160) return bpm / 3;
    }

    if (bpm < 50) {
        if (bpm * 2 >= 50 && bpm * 2 <= 160) return bpm * 2;
        if (bpm * 3 >= 50 && bpm * 3 <= 160) return bpm * 3;
    }

    return bpm;
}

// Custom mode calculation to pick the dominant BPM
int find_dominant_bpm(const std::map<int, int>& histo) {
    int mode_bpm = -1;
    int max_count = 0;

    std::map<int, int>::const_iterator it;
    for (it = histo.begin(); it != histo.end(); ++it) {
        if (it->second > max_count) {
            max_count = it->second;
            mode_bpm = it->first;
        }
    }

    int best_alt = -1;
    int best_alt_count = 0;

    for (it = histo.begin(); it != histo.end(); ++it) {
        int bpm = it->first;
        int count = it->second;

        // Skip candidates too close to the mode
        if (bpm >= mode_bpm - 15 && bpm < mode_bpm) continue;

        int before = histo.count(bpm - 1) ? histo.at(bpm - 1) : 0;
        int after  = histo.count(bpm + 1) ? histo.at(bpm + 1) : 0;

        bool is_sharp_peak = (count > before + 10) && (count > after + 10);

        bool strong_enough =
            (count >= (int)(max_count * 0.9)) ||    // standard strong override
            (count >= 25 &&
             histo.count(mode_bpm - 1) > 0 &&
             histo.count(mode_bpm + 1) > 0);        // mode is part of a noisy blob

        if (is_sharp_peak && strong_enough && bpm < mode_bpm) {
            best_alt = bpm;
            best_alt_count = count;
        }
    }

    if (best_alt > 0) {
        return best_alt;
    }

    return mode_bpm;
}

// Analyze the BPM of an audio file
float analyze_bpm(const char* filename) {
    uint_t win_s = 1024;
    uint_t hop_s = win_s / 4;
    unsigned int samplerate = 0;

    aubio_source_t* source = new_aubio_source(filename, samplerate, hop_s);
    if (!source) {
        std::cerr << "Error: could not open " << filename << std::endl;
        return 0;
    }

    samplerate = aubio_source_get_samplerate(source);

    aubio_tempo_t* tempo = new_aubio_tempo("default", win_s, hop_s, samplerate);
    if (!tempo) {
        std::cerr << "Error: could not create tempo object" << std::endl;
        del_aubio_source(source);
        return 0;
    }

    fvec_t* in = new_fvec(hop_s);
    fvec_t* out = new_fvec(2);

    uint_t read = 0;
    std::map<int, int> bpm_histogram;

    do {
        aubio_source_do(source, in, &read);
        aubio_tempo_do(tempo, in, out);

        if (out->data[0] != 0) {
            float bpm = aubio_tempo_get_bpm(tempo);
            if (bpm >= 40.0f && bpm <= 300.0f) {
                // Round down, based on comparison to other tools
                int rounded = static_cast<int>(bpm - 0.5f);
                bpm_histogram[rounded]++;
            }
        }
    } while (read == hop_s);

    int mode_bpm = find_dominant_bpm(bpm_histogram);

    del_fvec(in);
    del_fvec(out);
    del_aubio_tempo(tempo);
    del_aubio_source(source);

    if (mode_bpm > 0) {
        int normalized = normalize_bpm(mode_bpm);
        return static_cast<float>(normalized);
    }

    return 0.0f;
}

int main(int argc, char** argv) {
    if (argc != 2) {
        std::cerr << "Usage: " << argv[0] << " <filename>" << std::endl;
        return 1;
    }

    float bpm = analyze_bpm(argv[1]);
    if (bpm > 0) {
        std::cout << "BPM: " << static_cast<int>(bpm) << std::endl;
        return 0;
    }

    return 1;
}
